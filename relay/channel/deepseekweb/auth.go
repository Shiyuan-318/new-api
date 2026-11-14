package deepseekweb

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A channel credential is either
//
//	email:password     — the adaptor signs in automatically and keeps the
//	                     resulting userToken cached in-process, re-login on
//	                     expiry.
//	+86-13800138000:pw — mobile sign-in with an explicit area code.
//	13800138000:pw     — mobile sign-in, area code defaults to +86.
//	<userToken>        — a token lifted from localStorage.userToken of an
//	                     already signed-in browser session.
//
// Multiple credentials may be separated by newline inside one channel key
// (NewAPI multi-key handles comma splitting; newline lets passwords contain
// commas while staying inside a single key row).

// Credentials is one parsed credential entry.
type Credentials struct {
	Login    string // email or mobile, empty for token credentials
	Password string
	Token    string // pre-extracted userToken for token credentials
}

// IsAccount reports whether the credential performs password sign-in.
func (c Credentials) IsAccount() bool { return c.Login != "" }

// Identity is the stable per-credential identity used for device-id
// derivation and token caching. Passwords never take part in it.
func (c Credentials) Identity() string {
	if c.IsAccount() {
		return "account:" + c.Login
	}
	return "token:" + c.Token
}

// Mask renders the credential for logs and error messages without ever
// leaking the password or full token.
func (c Credentials) Mask() string {
	if c.IsAccount() {
		return c.Login
	}
	if len(c.Token) <= 8 {
		return "***"
	}
	return c.Token[:4] + "..." + c.Token[len(c.Token)-4:]
}

var mobilePattern = regexp.MustCompile(`^\+?[0-9][0-9\- ]{4,}$`)

// ParseCredentials splits one raw credential string.
func ParseCredentials(raw string) (Credentials, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Credentials{}, errors.New("empty credential")
	}
	if strings.Contains(raw, ":") {
		login, password, ok := strings.Cut(raw, ":")
		login = strings.TrimSpace(login)
		if !ok || login == "" || strings.TrimSpace(password) == "" {
			return Credentials{}, errors.New("credential must be \"email:password\" or a user token")
		}
		if !strings.Contains(login, "@") && !mobilePattern.MatchString(login) {
			return Credentials{}, fmt.Errorf("invalid account login %q: expected email or international mobile", maskLogin(login))
		}
		return Credentials{Login: login, Password: strings.TrimSpace(password)}, nil
	}
	return Credentials{Token: raw}, nil
}

// ParseCredentialList splits a channel key into credentials. Entries are
// separated by newlines (passwords may contain commas and colons).
func ParseCredentialList(raw string) ([]Credentials, error) {
	var out []Credentials
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cred, err := ParseCredentials(line)
		if err != nil {
			return nil, err
		}
		out = append(out, cred)
	}
	if len(out) == 0 {
		return nil, errors.New("no credential found in channel key")
	}
	return out, nil
}

func maskLogin(login string) string {
	at := strings.Index(login, "@")
	if at > 2 {
		return login[:2] + "***" + login[at:]
	}
	if len(login) > 4 {
		return login[:2] + "***"
	}
	return "***"
}

// deviceIDFor derives a stable UUIDv4-shaped device id from the credential
// identity, so the same account always presents the same device fingerprint
// across restarts — random ids per request are a classic risk-control signal.
func deviceIDFor(identity string) string {
	sum := sha256.Sum256([]byte("deepseek-web-device:" + identity))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return formatUUID(b)
}

func formatUUID(b [16]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexdigits[v>>4], hexdigits[v&0x0f])
	}
	return string(out)
}

// account holds the live session state for one credential. The zero-value is
// signed out; Token() performs sign-in on demand and refreshes on failure.
type account struct {
	mu         sync.Mutex
	cred       Credentials
	deviceID   string
	token      string
	tokenAge   time.Time
	mutedUntil time.Time // zero when not muted
	banned     bool
	upstream   *webClient
}

// accountRegistry caches live accounts for the process lifetime. Keyed by
// credential identity (never by password).
var accountRegistry sync.Map // string -> *account

// accountFor returns the shared account state for a credential, creating it
// on first use.
func accountFor(cred Credentials, upstream *webClient) *account {
	key := cred.Identity()
	if v, ok := accountRegistry.Load(key); ok {
		return v.(*account)
	}
	acc := &account{
		cred:     cred,
		deviceID: deviceIDFor(cred.Identity()),
		upstream: upstream,
	}
	actual, loaded := accountRegistry.LoadOrStore(key, acc)
	if loaded {
		return actual.(*account)
	}
	return acc
}

// maxTokenAge forces a periodic re-login even when the upstream never
// rejects the token, mirroring how often real clients rotate sessions.
const maxTokenAge = 7 * 24 * time.Hour

// errNeedsRelogin marks upstream rejections that invalidate the cached token.
var errNeedsRelogin = errors.New("deepseek web: token rejected")

// Token returns a usable userToken, signing in when needed. Upstream calls
// should treat auth failures as errNeedsRelogin and call EnsureFreshToken.
func (a *account) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokenLocked(ctx)
}

func (a *account) tokenLocked(ctx context.Context) (string, error) {
	if a.token != "" && !a.banned && time.Since(a.tokenAge) < maxTokenAge {
		return a.token, nil
	}
	return a.signInLocked(ctx)
}

// EnsureFreshToken re-signs-in unconditionally (after the upstream rejected
// the cached token) and returns the new token.
func (a *account) EnsureFreshToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token = ""
	return a.signInLocked(ctx)
}

func (a *account) signInLocked(ctx context.Context) (string, error) {
	if a.banned {
		return "", fmt.Errorf("deepseek web: account %s is banned", a.cred.Mask())
	}
	if !a.mutedUntil.IsZero() && time.Now().Before(a.mutedUntil) {
		return "", fmt.Errorf("deepseek web: account %s is muted until %s",
			a.cred.Mask(), a.mutedUntil.Format(time.RFC3339))
	}
	if !a.mutedUntil.IsZero() && time.Now().After(a.mutedUntil) {
		a.mutedUntil = time.Time{} // mute window elapsed
	}

	var result loginResult
	var err error
	if a.cred.IsAccount() {
		result, err = a.upstream.signIn(ctx, a.deviceID, a.cred.Login, a.cred.Password)
	} else {
		result, err = a.upstream.checkToken(ctx, a.deviceID, a.cred.Token)
	}
	if err != nil {
		return "", err
	}
	a.token = result.Token
	a.tokenAge = time.Now()
	a.mutedUntil = result.MutedUntil
	a.banned = result.Banned
	if a.banned {
		return "", fmt.Errorf("deepseek web: account %s is banned", a.cred.Mask())
	}
	return a.token, nil
}

// Invalidate drops the cached token after an upstream rejection.
func (a *account) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token = ""
}
