package deepseekweb

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// Vectors captured from the live web client: the web client produced exactly
// these (salt, expire_at, nonce) triples and the server accepted the
// resulting answers.
func TestDeepSeekHashV1Vectors(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"a40318e6b7af56bf27cf_1790959021012_58595", "168efa309e5a73033fc82dde4333bbc6515da440ef961574f73f99cbe7b397ad"},
		{"0b846826f9b7ab6000f6_1790959592240_44460", "ab9178c48249ac301272e3e24b9a5aef207e593add3003bfd7a8fb8abb572079"},
	}
	for i, c := range cases {
		got := deepSeekHashV1([]byte(c.msg))
		if hex.EncodeToString(got[:]) != c.want {
			t.Fatalf("vector %d: got %s want %s", i, hex.EncodeToString(got[:]), c.want)
		}
	}
}

func TestPowMessage(t *testing.T) {
	got := string(powMessage("a40318e6b7af56bf27cf", 1790959021012, 58595))
	want := "a40318e6b7af56bf27cf_1790959021012_58595"
	if got != want {
		t.Fatalf("powMessage: got %q want %q", got, want)
	}
}

func TestSolvePow(t *testing.T) {
	c := PowChallenge{
		Challenge:  "168efa309e5a73033fc82dde4333bbc6515da440ef961574f73f99cbe7b397ad",
		Salt:       "a40318e6b7af56bf27cf",
		ExpireAt:   1790959021012,
		Difficulty: 144000,
	}
	start := time.Now()
	nonce, err := c.Solve()
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("solve took too long: %s", elapsed)
	}
	solved := deepSeekHashV1(powMessage(c.Salt, c.ExpireAt, nonce))
	if hex.EncodeToString(solved[:]) != c.Challenge {
		t.Fatalf("solved nonce %d does not verify", nonce)
	}
}

func TestSolvePowRejectsUnknownAlgorithm(t *testing.T) {
	c := PowChallenge{Algorithm: "SomethingElse"}
	if _, err := c.Solve(); err == nil {
		t.Fatal("expected error for unknown algorithm")
	}
}

func TestPowResponseHeader(t *testing.T) {
	ch := PowChallenge{
		Algorithm:  "DeepSeekHashV1",
		Challenge:  "abc",
		Salt:       "s",
		Signature:  "sig",
		TargetPath: "/api/v0/chat/completion",
	}
	header, err := PowResponseHeader(&ch, 42)
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		t.Fatalf("header not base64: %v", err)
	}
	var parsed struct {
		Algorithm string `json:"algorithm"`
		Answer    uint64 `json:"answer"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		t.Fatalf("header not json: %v", err)
	}
	if parsed.Algorithm != "DeepSeekHashV1" || parsed.Answer != 42 || parsed.Signature != "sig" {
		t.Fatalf("unexpected header payload: %+v", parsed)
	}
}
