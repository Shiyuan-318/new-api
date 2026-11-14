package deepseekweb

import (
	"strings"
	"testing"
)

// Frame sequences below are reproduced from the live-protocol analysis of
// chat.deepseek.com (docs captured 2026-10).
func TestDrainStreamFullSequence(t *testing.T) {
	stream := "event: ready\n" +
		`data: {"request_message_id":1,"response_message_id":2,"model_type":"default"}` + "\n\n" +
		"event: update_session\n" +
		`data: {"updated_at":1}` + "\n\n" +
		`data: {"v":{"response":{"fragments":[{"id":2,"type":"THINK","content":"我们需要","x":1}]}}}` + "\n\n" +
		`data: {"p":"response/fragments/-1/content","o":"APPEND","v":"想清楚"}` + "\n\n" +
		`data: {"v":"再回答"}` + "\n\n" +
		`data: {"p":"response/fragments/-1/elapsed_secs","o":"SET","v":0.61}` + "\n\n" +
		`data: {"p":"response/fragments","o":"APPEND","v":[{"id":3,"type":"RESPONSE","content":"1"}]}` + "\n\n" +
		`data: {"p":"response/fragments/-1/content","v":"加"}` + "\n\n" +
		`data: {"v":"1等于2"}` + "\n\n" +
		`data: {"p":"response","o":"BATCH","v":[{"p":"accumulated_token_usage","v":74},{"p":"quasi_status","v":"FINISHED"}]}` + "\n\n" +
		`data: {"p":"response/status","o":"SET","v":"FINISHED"}` + "\n\n" +
		"event: title\n" +
		`data: {"content":"1加1等于2"}` + "\n\n" +
		"event: close\n" +
		`data: {"click_behavior":"none","auto_resume":false}` + "\n\n"

	var think, reply strings.Builder
	var usage int64
	finished := false
	cb := streamCallbacks{
		OnDelta: func(kind deltaKind, text string) {
			if kind == deltaThink {
				think.WriteString(text)
			} else {
				reply.WriteString(text)
			}
		},
		OnUsage:    func(total int64) { usage = total },
		OnFinished: func() { finished = true },
	}
	done, total, err := drainStream(strings.NewReader(stream), cb)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !done || !finished {
		t.Fatalf("stream must finish, done=%t finished=%t", done, finished)
	}
	if think.String() != "我们需要想清楚再回答" {
		t.Fatalf("think: %q", think.String())
	}
	if reply.String() != "1加1等于2" {
		t.Fatalf("reply: %q", reply.String())
	}
	if usage != 74 || total != 74 {
		t.Fatalf("usage: cb=%d total=%d", usage, total)
	}
}

func TestDrainStreamBareValueWithoutFragment(t *testing.T) {
	// A bare v with no preceding fragment must not crash the parser.
	stream := `data: {"v":"orphan"}` + "\n\n" + `data: {"p":"response/status","o":"SET","v":"FINISHED"}` + "\n\n"
	got := ""
	cb := streamCallbacks{OnDelta: func(kind deltaKind, text string) { got += text }}
	done, _, err := drainStream(strings.NewReader(stream), cb)
	if err != nil || !done {
		t.Fatalf("drain: err=%v done=%t", err, done)
	}
	if got != "" {
		t.Fatalf("orphan text must be dropped, got %q", got)
	}
}

func TestDrainStreamUnknownFramesTolerated(t *testing.T) {
	stream := "data: {not json}\n\n" +
		`data: {"p":"response/fragments","o":"APPEND","v":[{"id":1,"type":"RESPONSE","content":"hi"}]}` + "\n\n" +
		"data: {also not json}\n\n" +
		`data: {"p":"response/status","o":"SET","v":"FINISHED"}` + "\n\n"
	got := ""
	cb := streamCallbacks{OnDelta: func(kind deltaKind, text string) { got += text }}
	_, _, err := drainStream(strings.NewReader(stream), cb)
	if err != nil {
		t.Fatalf("unknown frames must be tolerated: %v", err)
	}
	if got != "hi" {
		t.Fatalf("got %q", got)
	}
}

func TestDrainStreamMidStreamErrorKeepsPartialOutput(t *testing.T) {
	stream := `data: {"v":{"response":{"fragments":[{"id":1,"type":"RESPONSE","content":"partial"}]}}}` + "\n\n" +
		`data: {"v":" answer"}` + "\n\n"
	got := ""
	cb := streamCallbacks{OnDelta: func(kind deltaKind, text string) { got += text }}
	finished, _, err := drainStream(strings.NewReader(stream), cb)
	if err == nil {
		t.Fatal("expected stream error for unterminated body")
	}
	if finished {
		t.Fatal("must not report finished")
	}
	if got != "partial answer" {
		t.Fatalf("partial output lost: %q", got)
	}
}

func TestReadSSEFramesMultiLineData(t *testing.T) {
	stream := "event: ready\n" +
		"data: {\"a\":\n" +
		"data: 1}\n\n" +
		`data: {"p":"response/status","o":"SET","v":"FINISHED"}` + "\n\n"
	var frames []sseFrame
	err := readSSEFrames(strings.NewReader(stream), func(f sseFrame) (bool, error) {
		frames = append(frames, f)
		return false, nil
	})
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames: %+v", len(frames), frames)
	}
	if frames[0].event != "ready" || frames[0].data != "{\"a\":\n1}" {
		t.Fatalf("first frame: %+v", frames[0])
	}
}
