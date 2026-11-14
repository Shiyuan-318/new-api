package deepseekweb

import (
	"strings"
	"testing"
)

func TestParseCredentials(t *testing.T) {
	cases := []struct {
		raw     string
		isAcc   bool
		wantErr bool
	}{
		{"user@example.com:secret", true, false},
		{"user@example.com:pa:ss:word", true, false}, // password keeps colons
		{"+86-13800138000:secret", true, false},
		{"13800138000:secret", true, false}, // bare mobile defaults to +86
		{"user@example.com:", false, true},
		{":secret", false, true},
		{"", false, true},
		{"  user@example.com: secret  ", true, false},
	}
	for _, c := range cases {
		cred, err := ParseCredentials(c.raw)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error", c.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.raw, err)
		}
		if cred.IsAccount() != c.isAcc {
			t.Fatalf("%q: IsAccount=%t", c.raw, cred.IsAccount())
		}
	}
}

func TestParseCredentialList(t *testing.T) {
	creds, err := ParseCredentialList("a@b.com:p1\ntokentoken\nc@d.com:p2")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(creds) != 3 {
		t.Fatalf("got %d creds", len(creds))
	}
	if !creds[0].IsAccount() || creds[1].IsAccount() {
		t.Fatalf("kinds wrong: %+v", creds)
	}
	if _, err := ParseCredentialList("\n \n"); err == nil {
		t.Fatal("empty list must error")
	}
}

func TestCredentialsMaskNeverLeaksSecret(t *testing.T) {
	acc := Credentials{Login: "user@example.com", Password: "super-secret-value"}
	if strings.Contains(acc.Mask(), "super-secret-value") {
		t.Fatal("mask leaks password")
	}
	tok := Credentials{Token: "0123456789abcdef0123456789abcdef"}
	masked := tok.Mask()
	if strings.Contains(masked, "0123456789abcdef0123456789abcdef") {
		t.Fatal("mask leaks token")
	}
	if !strings.HasPrefix(masked, "0123") || !strings.HasSuffix(masked, "cdef") {
		t.Fatalf("unexpected mask shape: %q", masked)
	}
}

func TestDeviceIDStableAndWellFormed(t *testing.T) {
	a := deviceIDFor("account:user@example.com")
	b := deviceIDFor("account:user@example.com")
	c := deviceIDFor("account:other@example.com")
	if a != b {
		t.Fatal("device id must be stable")
	}
	if a == c {
		t.Fatal("device id must differ per identity")
	}
	if len(a) != 36 || strings.Count(a, "-") != 4 {
		t.Fatalf("not uuid-shaped: %q", a)
	}
	if a[14] != '4' {
		t.Fatalf("must be uuid v4: %q", a)
	}
}

func TestPickCredentialRotates(t *testing.T) {
	creds := []Credentials{
		{Login: "a@b.com", Password: "p"},
		{Login: "c@d.com", Password: "p"},
		{Login: "e@f.com", Password: "p"},
	}
	seen := make(map[string]int)
	for i := 0; i < 9; i++ {
		cred := pickCredential(creds)
		seen[cred.Login]++
	}
	for login, n := range seen {
		if n != 3 {
			t.Fatalf("%s picked %d times, want 3", login, n)
		}
	}
}
