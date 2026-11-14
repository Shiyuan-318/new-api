// Package deepseekweb implements the DeepSeek Web channel: it exposes the
// free chat.deepseek.com web chat as an OpenAI-compatible relay channel.
//
// Unlike API-backed channels this adaptor cannot simply forward a request —
// every relay call performs a browser-shaped sequence upstream:
//
//	sign in (or reuse the cached userToken)
//	  → upload image attachments (own proof-of-work)
//	  → open a chat session
//	  → fetch + solve a proof-of-work challenge
//	  → POST /chat/completion and translate the SSE patch stream
//	  → delete the session in the background (throttled)
//
// The channel key holds either "email:password" credentials (auto sign-in)
// or a userToken lifted from a browser session; NewAPI multi-key rotation
// spreads load across accounts.
package deepseekweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ModelList names advertised to the channel form and tests. The suffixes map
// onto the web chat's thinking / search switches (see parseWebModelFlags).
var ModelList = []string{
	"deepseek-chat",
	"deepseek-reasoner",
	"deepseek-chat-search",
	"deepseek-reasoner-search",
}

const ChannelName = "deepseekweb"

// sessionDeletionInterval throttles background session deletes: the upstream
// rate-limits bursts of session operations (observed 429 under ~1 req/s).
const sessionDeletionInterval = 1600 * time.Millisecond

// completionTimeout bounds the whole upstream sequence per request. Streams
// inherit it through the request context, matching how long a single web
// turn may legally run.
const completionTimeout = 15 * time.Minute

// Adaptor implements channel.Adaptor for the DeepSeek web chat.
type Adaptor struct{}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	// The real URL is built inside DoRequest (multi-step flow); this only
	// serves logging and the generic relay plumbing.
	base := strings.TrimRight(info.ChannelBaseUrl, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return base + pathCompletion, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	// Headers are assembled per upstream step inside DoRequest.
	return nil
}

func (a *Adaptor) GetModelList() []string { return ModelList }

func (a *Adaptor) GetChannelName() string { return ChannelName }

// ConvertOpenAIRequest flattens the OpenAI request into the web payload.
func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	return convertOpenAIRequest(request)
}

// ConvertClaudeRequest converts Anthropic-format requests by normalizing
// them into an OpenAI request first — the web chat has one prompt surface.
func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	openaiReq := &dto.GeneralOpenAIRequest{Model: info.UpstreamModelName}
	for _, msg := range request.Messages {
		switch msg.Role {
		case "user":
			openaiReq.Messages = append(openaiReq.Messages, dto.Message{Role: "user", Content: messageContentFromClaude(msg.Content)})
		case "assistant":
			openaiReq.Messages = append(openaiReq.Messages, dto.Message{Role: "assistant", Content: messageContentFromClaude(msg.Content)})
		case "system":
			openaiReq.Messages = append(openaiReq.Messages, dto.Message{Role: "system", Content: messageContentFromClaude(msg.Content)})
		default:
			return nil, fmt.Errorf("the DeepSeek web channel does not support Claude role %q", msg.Role)
		}
	}
	return convertOpenAIRequest(openaiReq)
}

func messageContentFromClaude(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if m["type"] == "text" {
					if text, ok := m["text"].(string); ok {
						if b.Len() > 0 {
							b.WriteString("\n")
						}
						b.WriteString(text)
					}
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("the DeepSeek web channel does not support rerank")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("the DeepSeek web channel does not support embeddings")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("the DeepSeek web channel does not support audio")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("the DeepSeek web channel does not support image generation")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return nil, errors.New("the DeepSeek web channel does not support the Responses API")
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, errors.New("the DeepSeek web channel does not support Gemini requests")
}

// credentialPicker round-robins across credentials inside one key. Keys
// normally hold a single credential (multi-key rotation handles spreading);
// multi-line keys still work without it.
var credentialCounter atomic.Uint64

func pickCredential(creds []Credentials) Credentials {
	if len(creds) <= 1 {
		if len(creds) == 1 {
			return creds[0]
		}
		return Credentials{}
	}
	return creds[credentialCounter.Add(1)%uint64(len(creds))]
}

// DoRequest performs the full upstream sequence and returns the completion
// response. Any upstream failure is converted into an OpenAI-shaped error
// response so the relay error handler can map status codes (429 retry, 401
// disable) and surface messages verbatim.
func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	payload, err := decodePayload(requestBody)
	if err != nil {
		return nil, err
	}
	// Expose the flattened prompt and its token estimate to DoResponse for
	// usage accounting (the upstream never reports input tokens).
	c.Set(ctxKeyPrompt, payload.Prompt)
	c.Set(ctxKeyPromptTokens, service.CountTokenInput(payload.Prompt, info.UpstreamModelName))

	base := strings.TrimRight(info.ChannelBaseUrl, "/")
	httpClient, err := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if err != nil {
		return nil, fmt.Errorf("deepseek web: build http client: %w", err)
	}
	upstream := newWebClient(httpClient, base)

	creds, err := ParseCredentialList(info.ApiKey)
	if err != nil {
		return nil, err
	}
	account := accountFor(pickCredential(creds), upstream)

	ctx, cancel := context.WithTimeout(c.Request.Context(), completionTimeout)
	defer cancel()

	resp, err := a.runCompletion(ctx, c, info, upstream, account, payload)
	if err != nil {
		resp = a.errorResponse(err)
	}
	if info.IsStream {
		helper.SetEventStreamHeaders(c)
		if generalSettings := operationSettingPingInterval(); generalSettings > 0 {
			stop := helper.StartStreamKeepAlive(c, generalSettings)
			defer stop()
		}
	}
	return resp, nil
}

// runCompletion executes token → upload → session → pow → completion.
func (a *Adaptor) runCompletion(ctx context.Context, c *gin.Context, info *relaycommon.RelayInfo, upstream *webClient, account *account, payload *payloadRequest) (*http.Response, error) {
	token, err := account.Token(ctx)
	if err != nil {
		return nil, err
	}

	deviceID := account.deviceID
	var refFileIDs []string
	for _, ref := range payload.Images {
		fileID, err := uploadImage(ctx, upstream, account, token, ref)
		if err != nil {
			if errors.Is(err, errNeedsRelogin) {
				// Re-sign-in once and retry the upload.
				token, err = account.EnsureFreshToken(ctx)
				if err != nil {
					return nil, err
				}
				fileID, err = uploadImage(ctx, upstream, account, token, ref)
			}
			if err != nil {
				return nil, err
			}
		}
		refFileIDs = append(refFileIDs, fileID)
	}

	sessionID, err := upstream.createSession(ctx, token, deviceID)
	if err != nil {
		if errors.Is(err, errNeedsRelogin) {
			token, err = account.EnsureFreshToken(ctx)
			if err != nil {
				return nil, err
			}
			sessionID, err = upstream.createSession(ctx, token, deviceID)
		}
		if err != nil {
			return nil, err
		}
	}
	defer enqueueSessionDelete(upstream, account, token, sessionID)

	challenge, err := upstream.createPowChallenge(ctx, token, deviceID, completionTargetPath)
	if err != nil {
		return nil, err
	}
	_, powHeader, err := solveChallenge(challenge)
	if err != nil {
		return nil, err
	}

	comp := completionPayload{
		ChatSessionID:   sessionID,
		ParentMessageID: nil,
		ModelType:       strPtr(webModelTypeDefault),
		Prompt:          payload.Prompt,
		RefFileIDs:      refFileIDs,
		ThinkingEnabled: payload.ThinkingEnabled,
		SearchEnabled:   payload.SearchEnabled,
		Action:          nil,
		Preempt:         false,
	}
	resp, err := upstream.completion(ctx, token, deviceID, powHeader, comp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		token, err = account.EnsureFreshToken(ctx)
		if err != nil {
			return nil, err
		}
		return upstream.completion(ctx, token, deviceID, powHeader, comp)
	}
	return resp, nil
}

// uploadImage downloads or decodes an image reference, uploads it through
// the file API and waits until parsing completes; only parsed files can be
// referenced by a completion.
func uploadImage(ctx context.Context, upstream *webClient, account *account, token, ref string) (string, error) {
	var filename string
	var data []byte
	if strings.HasPrefix(ref, "data:") {
		mime, decoded, err := decodeDataURL(ref)
		if err != nil {
			return "", err
		}
		filename = "image." + mimeExt(mime)
		data = decoded
	} else {
		downloaded, err := downloadImage(ctx, ref)
		if err != nil {
			return "", err
		}
		filename, data = downloaded.name, downloaded.data
	}
	fileID, err := upstream.uploadFile(ctx, token, account.deviceID, filename, sniffImageMime(data), data)
	if err != nil {
		return "", err
	}
	if err := upstream.waitFileReady(ctx, token, account.deviceID, fileID); err != nil {
		return "", err
	}
	return fileID, nil
}

type downloadedImage struct {
	name string
	data []byte
}

// downloadImage fetches an http(s) image for re-upload.
func downloadImage(ctx context.Context, url string) (*downloadedImage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("deepseek web: bad image url: %w", err)
	}
	req.Header.Set("user-agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek web: download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deepseek web: download image: http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, fileUploadSizeLimit+1))
	if err != nil {
		return nil, fmt.Errorf("deepseek web: read image: %w", err)
	}
	if len(data) > fileUploadSizeLimit {
		return nil, fmt.Errorf("deepseek web: image too large (limit %d bytes)", fileUploadSizeLimit)
	}
	mime := sniffImageMime(data)
	if mime == "" {
		return nil, errors.New("deepseek web: unsupported image content type")
	}
	return &downloadedImage{name: "image." + mimeExt(mime), data: data}, nil
}

func sniffImageMime(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 6 && (string(data[:3]) == "GIF" && (data[3] == '8')):
		return "image/gif"
	case len(data) >= 12 && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}

func mimeExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "bin"
	}
}

// ── session deletion ────────────────────────────────────────────────────

// deleteQueue serializes background session deletions behind a ticker so a
// burst of relay calls never trips the account-level session rate limit.
// Overflow is dropped silently: an extra web-session is harmless.
var (
	deleteMu      sync.Mutex
	deletePending []sessionRef
	deleteRunning bool
)

type sessionRef struct {
	upstream  *webClient
	account   *account
	token     string
	sessionID string
}

func enqueueSessionDelete(upstream *webClient, account *account, token, sessionID string) {
	if sessionID == "" {
		return
	}
	deleteMu.Lock()
	defer deleteMu.Unlock()
	deletePending = append(deletePending, sessionRef{upstream: upstream, account: account, token: token, sessionID: sessionID})
	if deleteRunning {
		return
	}
	deleteRunning = true
	go deleteDrain()
}

func deleteDrain() {
	ticker := time.NewTicker(sessionDeletionInterval)
	defer ticker.Stop()
	for {
		deleteMu.Lock()
		if len(deletePending) == 0 {
			deleteRunning = false
			deleteMu.Unlock()
			return
		}
		ref := deletePending[0]
		deletePending = deletePending[1:]
		deleteMu.Unlock()

		<-ticker.C
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := ref.upstream.deleteSession(ctx, ref.token, ref.account.deviceID, ref.sessionID)
		cancel()
		if err != nil && !errors.Is(err, errNeedsRelogin) {
			common.SysError(fmt.Sprintf("deepseek web: delete session %s: %s", ref.sessionID, err.Error()))
		}
	}
}

// ── payload decode ──────────────────────────────────────────────────────

func decodePayload(requestBody io.Reader) (*payloadRequest, error) {
	raw, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, fmt.Errorf("deepseek web: read request body: %w", err)
	}
	var payload payloadRequest
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("deepseek web: bad internal payload: %w", err)
	}
	if payload.Prompt == "" {
		return nil, errors.New("deepseek web: prompt is empty")
	}
	return &payload, nil
}

func strPtr(s string) *string { return &s }

// ── error response shaping ──────────────────────────────────────────────

// errorResponse renders an upstream failure as an OpenAI error body with a
// mapped status code so the relay layer (retry logic, client message) sees
// a familiar shape.
func (a *Adaptor) errorResponse(err error) *http.Response {
	status := http.StatusBadGateway
	msg := err.Error()
	switch {
	case errors.Is(err, errNeedsRelogin):
		status = http.StatusUnauthorized
	case isRateLimitError(err):
		status = http.StatusTooManyRequests
	}
	body, marshalErr := common.Marshal(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "deepseek_web_error",
			"code":    status,
		},
	})
	if marshalErr != nil {
		body = []byte(fmt.Sprintf(`{"error":{"message":%q,"type":"deepseek_web_error"}}`, msg))
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func isRateLimitError(err error) bool {
	var biz *bizError
	if errors.As(err, &biz) {
		return strings.Contains(strings.ToLower(biz.Msg), "rate") ||
			strings.Contains(strings.ToLower(biz.Msg), "limit")
	}
	return strings.Contains(strings.ToLower(err.Error()), "too many requests") ||
		strings.Contains(strings.ToLower(err.Error()), "rate limit")
}

// operationSettingPingInterval mirrors the generic relay keep-alive setup in
// relay/channel/api_request.go.
func operationSettingPingInterval() time.Duration {
	generalSettings := operation_setting.GetGeneralSetting()
	if !generalSettings.PingIntervalEnabled {
		return 0
	}
	return time.Duration(generalSettings.PingIntervalSeconds) * time.Second
}

// Ensure the adaptor satisfies the interface at compile time.
var _ channel.Adaptor = (*Adaptor)(nil)
