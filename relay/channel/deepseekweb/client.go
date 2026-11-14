package deepseekweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// The web client talks to chat.deepseek.com exactly like the browser does:
// JSON bodies wrapped in a {code,msg,data:{biz_code,biz_msg,biz_data}}
// envelope, browser-shaped headers (the gateway soft-blocks requests without
// them), and proof-of-work headers on completion/upload.

const (
	defaultBaseURL = "https://chat.deepseek.com"

	pathLogin             = "/api/v0/users/login"
	pathAuthTokenCheck    = "/api/v0/users/auth_token/check_device"
	pathCreateSession     = "/api/v0/chat_session/create"
	pathDeleteSession     = "/api/v0/chat_session/delete"
	pathCreatePow         = "/api/v0/chat/create_pow_challenge"
	pathCompletion        = "/api/v0/chat/completion"
	pathUploadFile        = "/api/v0/file/upload_file"
	pathFetchFiles        = "/api/v0/file/fetch_files"
	pathSignInReferer     = defaultBaseURL + "/sign_in"
	clientBundleID        = "com.deepseek.chat"
	clientPlatform        = "web"
	clientVersion         = "2.5.0"
	clientLocale          = "zh_CN"
	clientTimezoneSeconds = 28800 // UTC+8, in seconds per the protocol
	userAgent             = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"

	// maxJSONResponse caps buffered non-stream responses (envelopes, file
	// listings) so a hostile upstream cannot exhaust memory.
	maxJSONResponse = 1 << 20

	// completionTargetPath / uploadTargetPath are the PoW target paths the
	// challenge must be requested and answered for.
	completionTargetPath = pathCompletion
	uploadTargetPath     = pathUploadFile
)

// webClient issues authenticated requests against one base URL. It is safe
// for concurrent use; per-account state lives in account.
type webClient struct {
	http    *http.Client
	baseURL string
}

func newWebClient(httpClient *http.Client, baseURL string) *webClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &webClient{http: httpClient, baseURL: baseURL}
}

// baseHeaders returns the browser-shaped header set every request carries.
// Missing these makes the gateway serve HTML challenge pages or 429s.
func (w *webClient) baseHeaders(deviceID string) http.Header {
	h := http.Header{}
	h.Set("accept", "*/*")
	h.Set("origin", w.baseURL)
	h.Set("referer", w.baseURL+"/")
	h.Set("user-agent", userAgent)
	h.Set("x-client-bundle-id", clientBundleID)
	h.Set("x-client-locale", clientLocale)
	h.Set("x-client-platform", clientPlatform)
	h.Set("x-client-timezone-offset", strconv.Itoa(clientTimezoneSeconds))
	h.Set("x-client-version", clientVersion)
	h.Set("x-device-id", deviceID)
	h.Set("x-device-model", "")
	return h
}

// doJSON performs one envelope request. When out is non-nil the biz_data
// payload is decoded into it.
func (w *webClient) doJSON(ctx context.Context, method, path, token, deviceID string, body any, extra http.Header, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("deepseek web: marshal %s body: %w", path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("deepseek web: build %s request: %w", path, err)
	}
	for k, vs := range w.baseHeaders(deviceID) {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("content-type", "application/json")
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("deepseek web: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponse))
	if err != nil {
		return fmt.Errorf("deepseek web: read %s response: %w", path, err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return errNeedsRelogin
	}
	if resp.StatusCode != http.StatusOK {
		return decodeUpstreamError(resp.StatusCode, data)
	}

	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("deepseek web: bad envelope from %s: %w", path, err)
	}
	if env.Code != 0 {
		return newBizError(env.Code, env.Msg)
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	var biz struct {
		BizCode int             `json:"biz_code"`
		BizMsg  string          `json:"biz_msg"`
		BizData json.RawMessage `json:"biz_data"`
	}
	if err := json.Unmarshal(env.Data, &biz); err != nil {
		return fmt.Errorf("deepseek web: bad data from %s: %w", path, err)
	}
	if biz.BizCode != 0 {
		return classifyBizError(biz.BizCode, biz.BizMsg)
	}
	if len(biz.BizData) == 0 {
		return nil
	}
	if err := json.Unmarshal(biz.BizData, out); err != nil {
		return fmt.Errorf("deepseek web: bad biz_data from %s: %w", path, err)
	}
	return nil
}

// bizError is an upstream failure with a stable code, letting the adaptor
// map risk-control states onto actionable client errors.
type bizError struct {
	Code int
	Msg  string
}

func (e *bizError) Error() string {
	return fmt.Sprintf("deepseek web: biz code %d: %s", e.Code, e.Msg)
}

func newBizError(code int, msg string) error {
	return &bizError{Code: code, Msg: msg}
}

// Upstream risk-control codes observed in the wild (biz_code):
// 5 = user muted, 10 = user banned, 11 = risky device.
const (
	bizCodeMuted       = 5
	bizCodeBanned      = 10
	bizCodeRiskyDevice = 11
)

func classifyBizError(code int, msg string) error {
	switch code {
	case bizCodeMuted, bizCodeBanned, bizCodeRiskyDevice:
		return errNeedsRelogin
	}
	return &bizError{Code: code, Msg: msg}
}

// decodeUpstreamError converts a non-200 response into an error, preferring
// the envelope message when present.
func decodeUpstreamError(status int, data []byte) error {
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(data, &env) == nil && env.Msg != "" {
		return fmt.Errorf("deepseek web: http %d: code %d: %s", status, env.Code, env.Msg)
	}
	return fmt.Errorf("deepseek web: http %d: %s", status, truncateForLog(data))
}

func truncateForLog(data []byte) string {
	const limit = 300
	if len(data) <= limit {
		return string(data)
	}
	return string(data[:limit]) + "..."
}

// loginResult carries the outcome of a sign-in (or token check).
type loginResult struct {
	Token      string
	MutedUntil time.Time
	Banned     bool
}

// chatStatus mirrors the user.chat block of the login response.
type chatStatus struct {
	IsMuted   json.Number `json:"is_muted"` // served as int or bool
	MuteUntil float64     `json:"mute_until"`
}

func (s chatStatus) muted() bool {
	return s.IsMuted.String() == "1" || s.IsMuted.String() == "true"
}

type loginUserInfo struct {
	Token string     `json:"token"`
	Chat  chatStatus `json:"chat"`
}

// signIn performs email/mobile password sign-in. The login response carries
// the fresh userToken plus the account's mute/ban state, so risk-controlled
// accounts are detected before any completion is attempted.
//
// Mobile logins use the international number; a leading "+<code>-" sets the
// area code explicitly, otherwise +86 is assumed.
func (w *webClient) signIn(ctx context.Context, deviceID, login, password string) (loginResult, error) {
	payload := map[string]string{
		"mobile":    "",
		"email":     "",
		"area_code": "",
		"os":        "web",
	}
	if strings.Contains(login, "@") {
		payload["email"] = login
	} else {
		mobile := login
		areaCode := "+86"
		if code, rest, found := strings.Cut(mobile, "-"); found && strings.HasPrefix(code, "+") {
			areaCode = code
			mobile = rest
		} else if strings.HasPrefix(mobile, "+") {
			mobile = strings.TrimPrefix(mobile, "+")
		}
		payload["mobile"] = mobile
		payload["area_code"] = areaCode
	}
	payload["password"] = password
	payload["device_id"] = deviceID

	extra := http.Header{}
	extra.Set("referer", pathSignInReferer)
	var out loginUserInfo
	if err := w.doJSON(ctx, http.MethodPost, pathLogin, "", deviceID, payload, extra, &out); err != nil {
		return loginResult{}, fmt.Errorf("deepseek web: sign in %s: %w", maskLogin(login), err)
	}
	if out.Token == "" {
		return loginResult{}, fmt.Errorf("deepseek web: sign in %s: empty token", maskLogin(login))
	}
	result := loginResult{Token: out.Token}
	if out.Chat.muted() && out.Chat.MuteUntil > 0 {
		result.MutedUntil = time.Unix(int64(out.Chat.MuteUntil), 0)
	}
	return result, nil
}

// checkToken validates a pre-extracted userToken via the device-check
// endpoint the real client calls after sign-in. It doubles as a token
// refresh probe for credential formats that cannot re-sign-in.
func (w *webClient) checkToken(ctx context.Context, deviceID, token string) (loginResult, error) {
	err := w.doJSON(ctx, http.MethodPost, pathAuthTokenCheck, token, deviceID,
		map[string]string{"device_id": deviceID, "device_model": ""}, nil, nil)
	if err != nil {
		return loginResult{}, fmt.Errorf("deepseek web: token check %s: %w", maskLogin(token), err)
	}
	return loginResult{Token: token}, nil
}

// createPowChallenge fetches a proof-of-work challenge for a target path.
func (w *webClient) createPowChallenge(ctx context.Context, token, deviceID, targetPath string) (*PowChallenge, error) {
	var out struct {
		Challenge PowChallenge `json:"challenge"`
	}
	err := w.doJSON(ctx, http.MethodPost, pathCreatePow, token, deviceID,
		map[string]string{"target_path": targetPath}, nil, &out)
	if err != nil {
		return nil, fmt.Errorf("deepseek web: fetch pow challenge: %w", err)
	}
	if out.Challenge.Challenge == "" {
		return nil, fmt.Errorf("deepseek web: empty pow challenge")
	}
	return &out.Challenge, nil
}

type createSessionResult struct {
	ChatSession struct {
		ID string `json:"id"`
	} `json:"chat_session"`
}

// createSession opens a chat session and returns its id.
func (w *webClient) createSession(ctx context.Context, token, deviceID string) (string, error) {
	var out createSessionResult
	if err := w.doJSON(ctx, http.MethodPost, pathCreateSession, token, deviceID,
		map[string]any{}, nil, &out); err != nil {
		return "", fmt.Errorf("deepseek web: create session: %w", err)
	}
	if out.ChatSession.ID == "" {
		return "", fmt.Errorf("deepseek web: empty session id")
	}
	return out.ChatSession.ID, nil
}

// deleteSession removes a chat session. The upstream rate-limits bursts of
// session operations, so callers must delete through the throttled queue.
func (w *webClient) deleteSession(ctx context.Context, token, deviceID, sessionID string) error {
	return w.doJSON(ctx, http.MethodPost, pathDeleteSession, token, deviceID,
		map[string]string{"chat_session_id": sessionID}, nil, nil)
}

// completionPayload is the request body of /chat/completion.
type completionPayload struct {
	ChatSessionID   string   `json:"chat_session_id"`
	ParentMessageID *int64   `json:"parent_message_id"`
	ModelType       *string  `json:"model_type"`
	Prompt          string   `json:"prompt"`
	RefFileIDs      []string `json:"ref_file_ids"`
	ThinkingEnabled bool     `json:"thinking_enabled"`
	SearchEnabled   bool     `json:"search_enabled"`
	Action          *string  `json:"action"`
	Preempt         bool     `json:"preempt"`
}

// completion starts the streamed completion. The caller owns resp.Body and
// must close it after draining the SSE stream.
func (w *webClient) completion(ctx context.Context, token, deviceID, powHeader string, payload completionPayload) (*http.Response, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("deepseek web: marshal completion: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+pathCompletion, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	for k, vs := range w.baseHeaders(deviceID) {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("authorization", "Bearer "+token)
	if powHeader != "" {
		req.Header.Set("x-ds-pow-response", powHeader)
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek web: completion: %w", err)
	}
	return resp, nil
}

// fileStatus values of the fetch_files state machine.
const (
	fileStatusSuccess       = "SUCCESS"
	fileStatusFailed        = "FAILED"
	fileStatusContentFilter = "CONTENT_FILTER"
	fileStatusTooLong       = "CONTENT_TOO_LONG"
	fileStatusCancelled     = "CANCELLED"
	fileParsePollInterval   = 600 * time.Millisecond
	fileParsePollTimeout    = 45 * time.Second
	fileUploadSizeLimit     = 20 << 20 // 20 MB
)

type fileInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// uploadFile uploads a file (image attachment) with its own proof-of-work
// and returns the file id once parsing starts.
func (w *webClient) uploadFile(ctx context.Context, token, deviceID, filename, contentType string, data []byte) (string, error) {
	if len(data) > fileUploadSizeLimit {
		return "", fmt.Errorf("deepseek web: file too large: %d bytes (limit %d)", len(data), fileUploadSizeLimit)
	}
	challenge, err := w.createPowChallenge(ctx, token, deviceID, uploadTargetPath)
	if err != nil {
		return "", err
	}
	_, powHeader, err := solveChallenge(challenge)
	if err != nil {
		return "", fmt.Errorf("deepseek web: solve upload pow: %w", err)
	}

	var buf bytes.Buffer
	mw := newMultipartWriter(&buf, filename, contentType, data)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL+pathUploadFile, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return "", err
	}
	for k, vs := range w.baseHeaders(deviceID) {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("content-type", mw.FormDataContentType())
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("x-ds-pow-response", powHeader)
	resp, err := w.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("deepseek web: upload file: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponse))
	if resp.StatusCode == http.StatusUnauthorized {
		return "", errNeedsRelogin
	}
	if resp.StatusCode != http.StatusOK {
		return "", decodeUpstreamError(resp.StatusCode, body)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			BizCode int `json:"biz_code"`
			BizData struct {
				ID   string `json:"id"`
				File struct {
					ID string `json:"id"`
				} `json:"file"`
			} `json:"biz_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("deepseek web: bad upload envelope: %w", err)
	}
	if env.Code != 0 {
		return "", newBizError(env.Code, "")
	}
	id := env.Data.BizData.ID
	if id == "" {
		id = env.Data.BizData.File.ID
	}
	if id == "" {
		return "", fmt.Errorf("deepseek web: upload response has no file id")
	}
	return id, nil
}

// fetchFiles returns the current parse status of the given file ids.
func (w *webClient) fetchFiles(ctx context.Context, token, deviceID string, ids []string) ([]fileInfo, error) {
	var out struct {
		Files []fileInfo `json:"files"`
	}
	err := w.doJSON(ctx, http.MethodGet, pathFetchFiles+"?file_ids="+strings.Join(ids, ","), token, deviceID, nil, nil, &out)
	if err != nil {
		return nil, err
	}
	return out.Files, nil
}

// waitFileReady polls fetch_files until the file finished parsing.
func (w *webClient) waitFileReady(ctx context.Context, token, deviceID, id string) error {
	deadline := time.Now().Add(fileParsePollTimeout)
	for {
		files, err := w.fetchFiles(ctx, token, deviceID, []string{id})
		if err == nil {
			for _, f := range files {
				if f.ID != id {
					continue
				}
				switch f.Status {
				case fileStatusSuccess:
					return nil
				case fileStatusFailed, fileStatusContentFilter, fileStatusTooLong, fileStatusCancelled:
					return fmt.Errorf("deepseek web: file not usable: status %s", f.Status)
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("deepseek web: file parse timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(fileParsePollInterval):
		}
	}
}

// newMultipartWriter assembles the single-file multipart body the upload
// endpoint expects.
func newMultipartWriter(buf *bytes.Buffer, filename, contentType string, data []byte) *multipart.Writer {
	mw := multipart.NewWriter(buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	part, _ := mw.CreatePart(h)
	part.Write(data)
	mw.Close()
	return mw
}
