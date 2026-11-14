package deepseekweb

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func strMessage(role, text string) dto.Message {
	return dto.Message{Role: role, Content: text}
}

func TestParseWebModelFlags(t *testing.T) {
	cases := []struct {
		model    string
		thinking bool
		search   bool
	}{
		{"deepseek-chat", false, false},
		{"deepseek-reasoner", true, false},
		{"deepseek-reasoner-search", true, true},
		{"deepseek-chat-search", false, true},
		{"deepseek-r1", true, false},
		{"deepseek-web-reasoner", true, false},
		{"deepseek-thinking", true, false},
		{"gpt-4o", false, false},
	}
	for _, c := range cases {
		thinking, search := parseWebModelFlags(c.model)
		if thinking != c.thinking || search != c.search {
			t.Fatalf("%s: got (%t,%t) want (%t,%t)", c.model, thinking, search, c.thinking, c.search)
		}
	}
}

func TestConvertOpenAIRequestFoldsHistory(t *testing.T) {
	stream := true
	req := &dto.GeneralOpenAIRequest{
		Model: "deepseek-reasoner",
		Messages: []dto.Message{
			strMessage("system", "Be concise."),
			strMessage("user", "What is 1+1?"),
			strMessage("assistant", "2"),
			strMessage("user", "and +2?"),
		},
		Stream: &stream,
	}
	payload, err := convertOpenAIRequest(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !payload.ThinkingEnabled {
		t.Fatal("reasoner model must enable thinking")
	}
	if payload.SearchEnabled {
		t.Fatal("search must be off")
	}
	for _, marker := range []string{"[Instructions]", "Be concise.", "User: What is 1+1?", "Assistant: 2", "and +2?"} {
		if !strings.Contains(payload.Prompt, marker) {
			t.Fatalf("prompt missing %q:\n%s", marker, payload.Prompt)
		}
	}
	if !strings.HasSuffix(payload.Prompt, "and +2?") {
		t.Fatalf("final user turn must be the prompt tail:\n%s", payload.Prompt)
	}
	if !payload.Stream {
		t.Fatal("stream flag lost")
	}
}

func TestConvertOpenAIRequestRejectsTools(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model:    "deepseek-chat",
		Messages: []dto.Message{strMessage("user", "hi")},
		Tools:    []dto.ToolCallRequest{{}},
	}
	if _, err := convertOpenAIRequest(req); err == nil {
		t.Fatal("tools must be rejected")
	}
}

func TestConvertOpenAIRequestRejectsEmpty(t *testing.T) {
	if _, err := convertOpenAIRequest(nil); err == nil {
		t.Fatal("nil request must be rejected")
	}
	if _, err := convertOpenAIRequest(&dto.GeneralOpenAIRequest{Model: "deepseek-chat"}); err == nil {
		t.Fatal("empty messages must be rejected")
	}
}

func TestConvertOpenAIRequestExtractsImages(t *testing.T) {
	dataURL := "data:image/png;base64,iVBORw0KGgo="
	httpURL := "https://example.com/cat.png"
	req := &dto.GeneralOpenAIRequest{
		Model: "deepseek-chat",
		Messages: []dto.Message{{
			Role: "user",
			Content: []dto.MediaContent{
				{Type: "text", Text: "what is in these?"},
				{Type: "image_url", ImageUrl: map[string]any{"url": dataURL}},
				{Type: "image_url", ImageUrl: map[string]any{"url": httpURL}},
			},
		}},
	}
	payload, err := convertOpenAIRequest(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(payload.Images) != 2 || payload.Images[0] != dataURL || payload.Images[1] != httpURL {
		t.Fatalf("images not extracted: %+v", payload.Images)
	}
}

func TestConvertOpenAIRequestDropsHistoricalImages(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Model: "deepseek-chat",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []dto.MediaContent{
					{Type: "text", Text: "earlier turn"},
					{Type: "image_url", ImageUrl: map[string]any{"url": "https://example.com/old.png"}},
				},
			},
			strMessage("assistant", "ok"),
			strMessage("user", "continue"),
		},
	}
	payload, err := convertOpenAIRequest(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(payload.Images) != 0 {
		t.Fatalf("historical images must be dropped, got %+v", payload.Images)
	}
}

func TestExtractImageRef(t *testing.T) {
	if _, err := extractImageRef("ftp://x"); err == nil {
		t.Fatal("non-http url must be rejected")
	}
	if _, err := extractImageRef("data:application/pdf;base64,AAAA"); err == nil {
		t.Fatal("non-image data url must be rejected")
	}
	got, err := extractImageRef(map[string]any{"url": "https://x/y.png"})
	if err != nil || got != "https://x/y.png" {
		t.Fatalf("map url: %v %q", err, got)
	}
}

func TestDecodeDataURL(t *testing.T) {
	mime, data, err := decodeDataURL("data:image/png;base64," + "AAECAw==")
	if err != nil || mime != "image/png" || len(data) != 4 {
		t.Fatalf("decode: %v %s %d", err, mime, len(data))
	}
	if _, _, err := decodeDataURL("data:image/png,raw"); err == nil {
		t.Fatal("non-base64 data url must be rejected")
	}
}

func TestSniffImageMime(t *testing.T) {
	if sniffImageMime([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) != "image/png" {
		t.Fatal("png magic not detected")
	}
	if sniffImageMime([]byte{0xFF, 0xD8, 0xFF, 0xE0}) != "image/jpeg" {
		t.Fatal("jpeg magic not detected")
	}
	if sniffImageMime([]byte("plain text")) != "" {
		t.Fatal("text must not be detected as image")
	}
}
