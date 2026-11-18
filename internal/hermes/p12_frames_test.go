package hermes

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// capturedFrame is one line of /tmp/p12-frames.jsonl, captured from a real
// gateway turn on 2026-11-19. The fixture is a copy: the test must be
// deterministic, but the event names are exactly what the gateway sent.
type capturedFrame struct {
	Frame int             `json:"frame"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func loadCapturedFrames(t *testing.T) []capturedFrame {
	t.Helper()
	file, err := os.Open("testdata/p12-frames.jsonl")
	if err != nil {
		t.Fatalf("open captured frames: %v", err)
	}
	defer file.Close()

	var frames []capturedFrame
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 2<<20)
	for scanner.Scan() {
		var frame capturedFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatalf("decode captured frame: %v", err)
		}
		frames = append(frames, frame)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read captured frames: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("captured frames are empty")
	}
	return frames
}

func TestCapturedFramesNormalizeToTheExpectedKinds(t *testing.T) {
	frames := loadCapturedFrames(t)

	type want struct {
		frame  int
		kind   string
		name   string
		status string
	}
	// Only non-delta frames are listed: 32 deltas in between are one reply.
	expected := []want{
		{1, "started", "", ""},
		{2, "started", "", ""},
		{3, "tool", "terminal", "started"},
		{4, "tool", "terminal", "completed"},
		{5, "tool", "read_file", "started"},
		{6, "tool", "read_file", "completed"},
		{39, "thinking", "_thinking", ""},
		{40, "done", "", ""},
		{41, "done", "", ""},
		{42, "done", "", ""},
	}

	byFrame := make(map[int]Event, len(frames))
	var deltas strings.Builder
	for _, frame := range frames {
		ev := normalize(frame.Event, frame.Data, nil)
		byFrame[frame.Frame] = ev
		if ev.Kind == "delta" {
			deltas.WriteString(ev.Text)
		}
	}
	for _, want := range expected {
		ev, ok := byFrame[want.frame]
		if !ok {
			t.Fatalf("frame %d is missing", want.frame)
		}
		if ev.Kind != want.kind {
			t.Fatalf("frame %d (%s) kind = %q, want %q", want.frame, ev.OriginalKind, ev.Kind, want.kind)
		}
		if want.name != "" && ev.Name != want.name {
			t.Fatalf("frame %d name = %q, want %q", want.frame, ev.Name, want.name)
		}
		if want.status != "" && ev.Status != want.status {
			t.Fatalf("frame %d status = %q, want %q", want.frame, ev.Status, want.status)
		}
	}

	if got := byFrame[3].Preview; !strings.Contains(got, "printf") {
		t.Fatalf("tool.started preview = %q, want the gateway's short detail", got)
	}
	if got := byFrame[39].Text; !strings.Contains(got, "Sudah, Boss") {
		t.Fatalf("thinking text = %q", got)
	}
	if got := deltas.String(); !strings.Contains(got, "Sudah, Boss") {
		t.Fatalf("assistant deltas did not carry the reply: %q", got)
	}
	if got := byFrame[41].Reasoning; !strings.Contains(got, "The user wants me to run a terminal command") {
		t.Fatalf("run.completed reasoning = %q", got)
	}
	// The private reasoning is not part of the reply text, even though the same
	// terminal frame carries it.
	if strings.Contains(deltas.String(), "The user wants me to run") {
		t.Fatal("reasoning leaked into assistant deltas")
	}
}

// TestCapturedThinkingIsNotDelta pins the one mapping that matters most: the
// _thinking block must be its own kind, so no caller can append it to a reply.
func TestCapturedThinkingIsNotDelta(t *testing.T) {
	for _, frame := range loadCapturedFrames(t) {
		ev := normalize(frame.Event, frame.Data, nil)
		if frame.Frame == 39 {
			if ev.Kind != "thinking" {
				t.Fatalf("frame 39 kind = %q, want thinking", ev.Kind)
			}
			return
		}
	}
	t.Fatal("frame 39 not found")
}
