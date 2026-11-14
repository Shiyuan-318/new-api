package deepseekweb

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
)

// Context keys carrying convert-time facts into DoResponse. The flattened
// prompt is what the upstream actually consumed, so token accounting must
// happen on it, not on the raw OpenAI messages.
const (
	ctxKeyPrompt         = "deepseekweb_prompt"
	ctxKeyPromptTokens   = "deepseekweb_prompt_tokens"
	ctxKeyCompletionText = "deepseekweb_completion_text"
)

// DoResponse translates the upstream SSE patch stream into an OpenAI
// response. Streaming clients receive delta chunks as fragments grow;
// non-streaming clients receive one assembled completion.
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp == nil || resp.Body == nil {
		return nil, types.NewError(errors.New("deepseek web: empty upstream response"), types.ErrorCodeBadResponse)
	}

	if resp.StatusCode != http.StatusOK {
		// Relay the upstream body through the standard error handler; the
		// body was already rendered as an OpenAI error by errorResponse.
		return nil, service.RelayErrorHandler(c.Request.Context(), resp, false)
	}

	model := info.UpstreamModelName
	if model == "" {
		model = "deepseek-chat"
	}
	responseID := "chatcmpl-" + randomHex(24)
	created := time.Now().Unix()

	if info.IsStream {
		return a.streamResponse(c, resp, info, model, responseID, created)
	}
	return a.jsonResponse(c, resp, info, model, responseID, created)
}

// streamResponse forwards THINK fragments as reasoning_content deltas and
// RESPONSE fragments as content deltas, ending with a stop chunk and DONE.
func (a *Adaptor) streamResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, model, responseID string, created int64) (any, *types.NewAPIError) {
	var reasoningLen, contentLen int
	var delivered bool

	emit := func(delta dto.ChatCompletionsStreamResponseChoiceDelta) {
		delivered = true
		chunk := dto.ChatCompletionsStreamResponse{
			Id:      responseID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []dto.ChatCompletionsStreamResponseChoice{{
				Delta: delta,
				Index: 0,
			}},
		}
		if err := helper.ObjectData(c, chunk); err != nil {
			// Client disconnected mid-stream: stop writing, keep draining so
			// billing still sees the upstream totals.
			delivered = false
		}
	}

	cb := streamCallbacks{
		OnDelta: func(kind deltaKind, text string) {
			if text == "" {
				return
			}
			switch kind {
			case deltaThink:
				reasoningLen += len(text)
				emit(dto.ChatCompletionsStreamResponseChoiceDelta{ReasoningContent: strPtr(text)})
			case deltaReply:
				contentLen += len(text)
				emit(dto.ChatCompletionsStreamResponseChoiceDelta{Content: strPtr(text)})
			}
		},
	}

	_, totalTokens, streamErr := drainStream(resp.Body, cb)
	usage := finalizeUsage(c, int(totalTokens), reasoningLen+contentLen)

	finishReason := "stop"
	if streamErr != nil && !delivered {
		// Nothing reached the client: surface the failure instead of an
		// empty completion.
		return nil, types.NewError(streamErr, types.ErrorCodeBadResponse)
	}
	if streamErr != nil {
		finishReason = ""
	}

	final := dto.ChatCompletionsStreamResponse{
		Id:      responseID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{{
			Delta:        dto.ChatCompletionsStreamResponseChoiceDelta{},
			FinishReason: strPtr(finishReason),
			Index:        0,
		}},
		Usage: usage,
	}
	if err := helper.ObjectData(c, final); err != nil {
		_ = err // client gone; drain already completed
	}
	if err := helper.StringData(c, "[DONE]"); err != nil {
		_ = err
	}
	return usage, nil
}

// jsonResponse assembles the full completion for non-stream clients.
func (a *Adaptor) jsonResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, model, responseID string, created int64) (any, *types.NewAPIError) {
	var reasoning, content strings.Builder

	cb := streamCallbacks{
		OnDelta: func(kind deltaKind, text string) {
			if text == "" {
				return
			}
			if kind == deltaThink {
				reasoning.WriteString(text)
			} else {
				content.WriteString(text)
			}
		},
	}

	_, totalTokens, streamErr := drainStream(resp.Body, cb)
	if content.Len() == 0 && reasoning.Len() == 0 && streamErr != nil {
		return nil, types.NewError(streamErr, types.ErrorCodeBadResponse)
	}

	usage := finalizeUsage(c, int(totalTokens), reasoning.Len()+content.Len())

	message := dto.Message{Role: "assistant", Content: content.String()}
	if reasoning.Len() > 0 {
		r := reasoning.String()
		message.ReasoningContent = &r
	}
	response := dto.OpenAITextResponse{
		Id:      responseID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []dto.OpenAITextResponseChoice{{
			Index:        0,
			Message:      message,
			FinishReason: "stop",
		}},
	}
	response.Usage = *usage
	return &response, nil
}

// finalizeUsage splits the upstream session-cumulative total into prompt and
// completion shares. The web chat never reports input tokens, so the prompt
// share comes from the convert-time estimate stored in the context; the
// remainder is attributed to the completion.
func finalizeUsage(c *gin.Context, totalTokens, outputChars int) *dto.Usage {
	usage := &dto.Usage{}
	promptTokens := c.GetInt(ctxKeyPromptTokens)
	if promptTokens <= 0 {
		if prompt := c.GetString(ctxKeyPrompt); prompt != "" {
			promptTokens = service.CountTokenInput(prompt, "deepseek-chat")
		}
	}
	if totalTokens > 0 {
		usage.TotalTokens = totalTokens
		usage.CompletionTokens = totalTokens - promptTokens
		if usage.CompletionTokens < 0 {
			// The estimate overshot the upstream total (tokenizer drift);
			// fall back to a char-based completion estimate.
			usage.CompletionTokens = outputChars / 4
			if usage.CompletionTokens < 1 {
				usage.CompletionTokens = 1
			}
		}
		usage.PromptTokens = usage.TotalTokens - usage.CompletionTokens
		if usage.PromptTokens < 0 {
			usage.PromptTokens = 0
		}
	} else {
		// Upstream never reported usage (abnormal close): estimate both.
		usage.PromptTokens = promptTokens
		usage.CompletionTokens = outputChars / 4
		if usage.CompletionTokens < 1 && outputChars > 0 {
			usage.CompletionTokens = 1
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
