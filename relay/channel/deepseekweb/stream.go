package deepseekweb

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The completion endpoint answers with a text/event-stream of JSON patches
// applied to the growing response object:
//
//	event: ready
//	data: {"request_message_id":1,"response_message_id":2,"model_type":"default"}
//
//	data: {"v":{"response":{...,"fragments":[{"id":2,"type":"THINK","content":"…"}]}}}
//	data: {"p":"response/fragments/-1/content","o":"APPEND","v":"…"}
//	data: {"v":"…"}                                    ← bare v continues the last patch
//	data: {"p":"response/fragments","o":"APPEND","v":[{...,"type":"RESPONSE",...}]}
//	data: {"p":"response","o":"BATCH","v":[{"p":"accumulated_token_usage","v":74},
//	                                      {"p":"quasi_status","v":"FINISHED"}]}
//	data: {"p":"response/status","o":"SET","v":"FINISHED"}
//
//	event: close
//	data: {"click_behavior":"none","auto_resume":false}
//
// Fragment type THINK carries reasoning (→ reasoning_content), RESPONSE the
// answer (→ content). accumulated_token_usage is session-cumulative; with
// one session per relay request it equals this request's total usage.

// fragment types observed in the patch stream.
const (
	fragmentTypeThink    = "THINK"
	fragmentTypeResponse = "RESPONSE"
	fragmentStatusPath   = "response/status"
	statusFinished       = "FINISHED"
)

// deltaKind routes a text chunk to its OpenAI counterpart.
type deltaKind int

const (
	deltaThink deltaKind = iota
	deltaReply
)

// streamCallbacks receive the parsed stream. All callbacks fire synchronously
// from the read loop.
type streamCallbacks struct {
	// OnDelta fires for every text chunk of a THINK or RESPONSE fragment.
	OnDelta func(kind deltaKind, text string)
	// OnUsage fires once with the session-cumulative token usage (the
	// request's total when one session serves one request).
	OnUsage func(totalTokens int64)
	// OnFinished fires when the upstream reported a terminal status.
	OnFinished func()
}

// sseFrame is one decoded Server-Sent Event.
type sseFrame struct {
	event string
	data  string
}

// readSSEFrames splits an SSE body into frames. The web protocol keeps one
// data line per frame; multiple data lines would be joined per the SSE spec.
func readSSEFrames(body io.Reader, handle func(frame sseFrame) (stop bool, err error)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // fragments can be large single lines

	var event string
	var dataLines []string
	flush := func() (bool, error) {
		if len(dataLines) == 0 && event == "" {
			return false, nil
		}
		frame := sseFrame{event: event, data: strings.Join(dataLines, "\n")}
		event = ""
		dataLines = nil
		if strings.TrimSpace(frame.data) == "" && frame.event == "" {
			return false, nil
		}
		return handle(frame)
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			stop, err := flush()
			if stop || err != nil {
				return err
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("deepseek web: read stream: %w", err)
	}
	_, err := flush()
	return err
}

// sseParser implements the patch state machine.
type sseParser struct {
	cb        *streamCallbacks
	lastPath  string
	fragTypes []string // fragment type per index, parallel to upstream ids
	finished  bool
	usage     int64
}

func (p *sseParser) handle(frame sseFrame) (bool, error) {
	if frame.event == "close" {
		p.finished = true
		return true, nil
	}
	if frame.event == "ready" || frame.event == "title" || frame.event == "update_session" {
		return false, nil
	}
	return p.applyPatch([]byte(frame.data))
}

// applyPatch interprets one JSON patch frame.
func (p *sseParser) applyPatch(data []byte) (bool, error) {
	if len(data) == 0 {
		return false, nil
	}
	var pv struct {
		P *string         `json:"p"`
		O string          `json:"o"`
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(data, &pv); err != nil {
		// Tolerate unknown frames instead of killing the stream.
		return false, nil
	}

	// Bare {"v": "..."} continues the previous patch path.
	if pv.P == nil {
		var s string
		if err := json.Unmarshal(pv.V, &s); err == nil {
			p.appendText(p.lastPath, s)
			return false, nil
		}
		// Bare {"v": {response object}} — the initial dump containing the
		// first THINK fragment.
		var dump struct {
			Response struct {
				Fragments []streamFragment `json:"fragments"`
			} `json:"response"`
		}
		if err := json.Unmarshal(pv.V, &dump); err == nil {
			for _, f := range dump.Response.Fragments {
				p.pushFragment(f)
			}
		}
		return false, nil
	}

	path := *pv.P
	p.lastPath = path
	switch {
	case path == "response/fragments" && pv.O == "APPEND":
		var frags []streamFragment
		if err := json.Unmarshal(pv.V, &frags); err == nil {
			for _, f := range frags {
				p.pushFragment(f)
			}
		}
	case path == "response" && pv.O == "BATCH":
		var subs []struct {
			P string          `json:"p"`
			V json.RawMessage `json:"v"`
		}
		if err := json.Unmarshal(pv.V, &subs); err == nil {
			for _, sub := range subs {
				p.applySubPatch(sub.P, sub.V)
			}
		}
	default:
		p.applySubPatch(path, pv.V)
	}
	return false, nil
}

func (p *sseParser) applySubPatch(path string, value json.RawMessage) {
	switch path {
	case "response/accumulated_token_usage", "accumulated_token_usage":
		var total int64
		if err := json.Unmarshal(value, &total); err == nil {
			p.usage = total
			if p.cb.OnUsage != nil {
				p.cb.OnUsage(total)
			}
		}
	case "response/quasi_status", "quasi_status", "response/status":
		var status string
		if err := json.Unmarshal(value, &status); err == nil && status == statusFinished {
			p.finished = true
			if p.cb.OnFinished != nil {
				p.cb.OnFinished()
			}
		}
	default:
		if strings.HasSuffix(path, "/content") {
			var s string
			if err := json.Unmarshal(value, &s); err == nil {
				p.appendText(path, s)
			}
		}
	}
}

// streamFragment mirrors one element of response.fragments.
type streamFragment struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

func (p *sseParser) pushFragment(f streamFragment) {
	p.fragTypes = append(p.fragTypes, f.Type)
	if f.Content != "" {
		p.emit(f.Type, f.Content)
	}
}

// appendText emits an appended chunk when the targeted fragment's type is
// known. "-1" addresses the last fragment.
func (p *sseParser) appendText(path, text string) {
	if text == "" || p.cb.OnDelta == nil {
		return
	}
	idx := len(p.fragTypes) - 1
	if idx < 0 {
		return
	}
	kind := deltaReply
	if len(p.fragTypes) > 0 && p.fragTypes[idx] == fragmentTypeThink {
		kind = deltaThink
	}
	p.cb.OnDelta(kind, text)
}

func (p *sseParser) emit(fragType, text string) {
	if p.cb.OnDelta == nil || text == "" {
		return
	}
	if fragType == fragmentTypeThink {
		p.cb.OnDelta(deltaThink, text)
		return
	}
	if fragType == fragmentTypeResponse || fragType == "" {
		p.cb.OnDelta(deltaReply, text)
	}
}

// drainStream parses the whole completion stream, returning whether the
// upstream reached a terminal state and the cumulative token usage (0 when
// the upstream never reported one). A body that ends without a terminal
// status is a truncated stream and surfaces as an error; callers may still
// have received partial output through the callbacks.
func drainStream(body io.Reader, cb streamCallbacks) (finished bool, totalTokens int64, err error) {
	parser := &sseParser{cb: &cb}
	stop := false
	streamErr := readSSEFrames(body, func(frame sseFrame) (bool, error) {
		stop, err = parser.handle(frame)
		return stop, err
	})
	switch {
	case streamErr != nil:
		// A mid-stream transport error: partial output was delivered and
		// the client must not be killed with a hard failure.
		return parser.finished, parser.usage, fmt.Errorf("deepseek web: %w", streamErr)
	case !parser.finished:
		return false, parser.usage, errors.New("deepseek web: stream ended without finish")
	default:
		return true, parser.usage, nil
	}
}
