package deepseekweb

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// The web chat exposes two independent switches (deep thinking, web search)
// on top of a single default model. OpenAI model names map onto them
// heuristically so common names keep working without channel-side mapping:
//
//	deepseek-chat                       → plain
//	deepseek-reasoner / -r1 / -think*   → thinking
//	*-search                            → search
//	deepseek-web-*                      → same, "web-" accepted and ignored

// payloadRequest is the intermediate JSON the Convert step emits and DoRequest
// consumes. Keeping it serializable preserves the relay framework's contract
// (Convert returns any, the framework marshals it into the outbound body).
type payloadRequest struct {
	Prompt          string   `json:"prompt"`
	ThinkingEnabled bool     `json:"thinking_enabled"`
	SearchEnabled   bool     `json:"search_enabled"`
	Images          []string `json:"images,omitempty"`
	// Stream mirrors the client's stream flag; relay already tracks it in
	// info.IsStream, it is kept here so DoRequest can double-check.
	Stream bool `json:"stream,omitempty"`
}

// parseWebModelFlags extracts the thinking/search switches from a model name.
func parseWebModelFlags(model string) (thinking bool, search bool) {
	name := strings.ToLower(model)
	name = strings.TrimPrefix(name, "deepseek-web-")
	name = strings.TrimPrefix(name, "deepseek-")
	if strings.Contains(name, "reasoner") || strings.Contains(name, "think") ||
		strings.Contains(name, "r1") || strings.Contains(name, "-t-") {
		thinking = true
	}
	if strings.Contains(name, "search") {
		search = true
	}
	return thinking, search
}

const webModelTypeDefault = "default"

// convertOpenAIRequest flattens an OpenAI chat request into a web prompt.
//
// The upstream is a conversational web chat: it accepts exactly one prompt
// per turn and keeps context server-side. This adaptor is stateless — each
// relay request opens a fresh session — so prior turns are folded into the
// prompt with explicit role markers.
//
// Tool calling is not available through the web chat; requests carrying
// tools are rejected instead of silently degraded.
func convertOpenAIRequest(request *dto.GeneralOpenAIRequest) (*payloadRequest, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if len(request.Tools) > 0 || request.FunctionCall != nil || len(request.Functions) > 0 {
		return nil, errors.New("the DeepSeek web channel does not support tool/function calling")
	}
	if len(request.Messages) == 0 {
		return nil, errors.New("messages is empty")
	}

	var prompt strings.Builder
	var images []string
	for i, msg := range request.Messages {
		last := i == len(request.Messages)-1
		switch msg.Role {
		case "system", "developer":
			if text := messageText(msg); text != "" {
				prompt.WriteString("[Instructions]\n")
				prompt.WriteString(text)
				prompt.WriteString("\n\n")
			}
		case "assistant":
			if text := messageText(msg); text != "" {
				prompt.WriteString("Assistant: ")
				prompt.WriteString(text)
				prompt.WriteString("\n\n")
			}
		case "user":
			text, msgImages, err := userMessageContent(msg, last)
			if err != nil {
				return nil, err
			}
			images = append(images, msgImages...)
			if last {
				// The final user turn is the actual prompt; no marker.
				prompt.WriteString(text)
			} else {
				prompt.WriteString("User: ")
				prompt.WriteString(text)
				prompt.WriteString("\n\n")
			}
		case "tool":
			return nil, errors.New("the DeepSeek web channel does not support tool calling")
		default:
			return nil, fmt.Errorf("unsupported message role: %s", msg.Role)
		}
	}

	if strings.TrimSpace(prompt.String()) == "" {
		return nil, errors.New("prompt is empty")
	}

	thinking, search := parseWebModelFlags(request.Model)
	return &payloadRequest{
		Prompt:          prompt.String(),
		ThinkingEnabled: thinking,
		SearchEnabled:   search,
		Images:          images,
		Stream:          request.Stream != nil && *request.Stream,
	}, nil
}

// messageText renders a message's content as plain text, ignoring images.
func messageText(msg dto.Message) string {
	if s, ok := msg.Content.(string); ok {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	for _, part := range msg.ParseContent() {
		if part.Type == "text" && part.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(strings.TrimSpace(part.Text))
		}
	}
	return b.String()
}

// userMessageContent extracts text and image references from one user
// message. Images are only honored on the final turn: the upstream uploads
// attachments per-message and historical images cannot be re-attached to a
// folded prompt.
func userMessageContent(msg dto.Message, last bool) (string, []string, error) {
	if s, ok := msg.Content.(string); ok {
		return strings.TrimSpace(s), nil, nil
	}
	var b strings.Builder
	var images []string
	for _, part := range msg.ParseContent() {
		switch part.Type {
		case "text":
			if part.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(strings.TrimSpace(part.Text))
			}
		case "image_url":
			if !last {
				continue
			}
			ref, err := extractImageRef(part.ImageUrl)
			if err != nil {
				return "", nil, err
			}
			images = append(images, ref)
		case "input_audio", "file", "video_url":
			return "", nil, fmt.Errorf("the DeepSeek web channel does not support %s content", part.Type)
		}
	}
	return b.String(), images, nil
}

// extractImageRef normalizes an OpenAI image_url part into either an
// http(s) URL or a "data:<mime>;base64,<payload>" reference. Both forms are
// downloaded/decoded and re-uploaded by DoRequest.
func extractImageRef(raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		return normalizeImageRef(v)
	case map[string]any:
		if url, ok := v["url"].(string); ok {
			return normalizeImageRef(url)
		}
		return "", errors.New("image_url part has no url field")
	default:
		return "", errors.New("unsupported image_url payload")
	}
}

func normalizeImageRef(url string) (string, error) {
	url = strings.TrimSpace(url)
	switch {
	case strings.HasPrefix(url, "data:image/"):
		return url, nil
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"):
		return url, nil
	case strings.HasPrefix(url, "data:"):
		return "", errors.New("only image data URLs are supported")
	default:
		return "", errors.New("image_url must be an http(s) URL or a data URL")
	}
}

// decodeDataURL splits a "data:<mime>;base64,<payload>" URL.
func decodeDataURL(ref string) (mime string, data []byte, err error) {
	const prefix = "data:"
	if !strings.HasPrefix(ref, prefix) {
		return "", nil, errors.New("not a data URL")
	}
	rest := ref[len(prefix):]
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", nil, errors.New("malformed data URL")
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	mime = strings.TrimSuffix(meta, ";base64")
	if !strings.HasSuffix(meta, ";base64") {
		return "", nil, errors.New("only base64 data URLs are supported")
	}
	data, err = base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("bad base64 image data: %w", err)
	}
	return mime, data, nil
}
