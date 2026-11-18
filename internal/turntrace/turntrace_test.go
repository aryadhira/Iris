package turntrace

import (
	"strings"
	"testing"
	"time"

	"github.com/aryadhira/iris/internal/hermes"
)

func TestStepsKeepOrderAndRealDurations(t *testing.T) {
	tracker := NewTracker()
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	observe := func(ev hermes.Event) []Step {
		t.Helper()
		return tracker.Observe(ev, at)
	}

	observe(hermes.Event{Kind: "tool", Name: "terminal", Status: "started", Preview: "printf 'x' + 1 command", TS: 100})
	observe(hermes.Event{Kind: "thinking", Text: "Saya perlu memeriksa berkasnya dulu.", TS: 101.5})
	observe(hermes.Event{Kind: "tool", Name: "terminal", Status: "completed", TS: 112.9})
	observe(hermes.Event{Kind: "tool", Name: "read_file", Status: "started", Preview: "/tmp/out.txt", TS: 113})
	observe(hermes.Event{Kind: "tool", Name: "read_file", Status: "failed", Preview: "tidak ditemukan", TS: 113.5})
	observe(hermes.Event{Kind: "done", Text: "selesai", Reasoning: "private reasoning block", TS: 114})

	steps := tracker.Steps()
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3 (%+v)", len(steps), steps)
	}
	wantKinds := []string{KindTool, KindThinking, KindTool}
	for i, step := range steps {
		if step.Seq != i+1 {
			t.Fatalf("step %d seq = %d, want %d", i, step.Seq, i+1)
		}
		if step.Kind != wantKinds[i] {
			t.Fatalf("step %d kind = %q, want %q", i, step.Kind, wantKinds[i])
		}
	}
	if steps[0].DurationMs != 12900 {
		t.Fatalf("terminal duration = %d ms, want 12900", steps[0].DurationMs)
	}
	if steps[0].Status != StatusOK || steps[0].EndedAt == nil {
		t.Fatalf("terminal step = %+v, want ok with an end", steps[0])
	}
	if steps[1].Status != StatusOK || steps[1].DurationMs != 11400 {
		t.Fatalf("thinking step = %+v, want closed by the next tool at 11.4 s", steps[1])
	}
	if steps[2].Status != StatusFailed {
		t.Fatalf("failed tool status = %q", steps[2].Status)
	}
	if steps[2].Detail != "tidak ditemukan" {
		t.Fatalf("failed tool detail = %q", steps[2].Detail)
	}
	if got := tracker.Reasoning(); !strings.Contains(got, "private reasoning block") {
		t.Fatalf("reasoning = %q", got)
	}
}

func TestThinkingIsCoalescedIntoOneStepAndNeverInvented(t *testing.T) {
	tracker := NewTracker()
	at := time.Now()
	tracker.Observe(hermes.Event{Kind: "thinking", Text: "satu", TS: 10}, at)
	tracker.Observe(hermes.Event{Kind: "thinking", Text: "dua", TS: 11}, at)
	steps := tracker.Steps()
	if len(steps) != 1 {
		t.Fatalf("thinking steps = %d, want 1", len(steps))
	}
	if !strings.Contains(steps[0].Detail, "satu dua") {
		t.Fatalf("thinking detail = %q", steps[0].Detail)
	}
	if len(tracker.Finish(at)) != 1 || steps[0].Status == StatusFailed {
		t.Fatalf("finish changed nothing for the open thinking step")
	}

	empty := NewTracker()
	empty.Observe(hermes.Event{Kind: "done", Text: "halo"}, at)
	if len(empty.Steps()) != 0 {
		t.Fatalf("a turn with no signals produced %d steps", len(empty.Steps()))
	}
}

func TestToolPreviewIsShortAndRedacted(t *testing.T) {
	tracker := NewTracker()
	tracker.Observe(hermes.Event{
		Kind:    "tool",
		Name:    "terminal",
		Status:  "started",
		Preview: `curl -H "Authorization: Bearer abcdefghijklmnop" https://x.test && API_KEY=sk-live-12345678`,
		TS:      1,
	}, time.Now())
	step := tracker.Steps()[0]
	if strings.Contains(step.Detail, "abcdefghijklmnop") || strings.Contains(step.Detail, "sk-live-12345678") {
		t.Fatalf("secret survived redaction: %q", step.Detail)
	}
	if len(step.Detail) > maxToolDetail+4 {
		t.Fatalf("detail is %d bytes, want short", len(step.Detail))
	}
}

func TestReasoningExcerptIsBoundedToOneBlob(t *testing.T) {
	tracker := NewTracker()
	block := "RAHASIA-P12 " + strings.Repeat("kerja ", 1000)
	at := time.Now()
	tracker.Observe(hermes.Event{Kind: "thinking", Text: block, TS: 1}, at)
	tracker.Observe(hermes.Event{Kind: "done", Reasoning: block + " tambahan", TS: 2}, at)
	if len(tracker.Reasoning()) > MaxReasoning {
		t.Fatalf("reasoning = %d bytes, want <= %d", len(tracker.Reasoning()), MaxReasoning)
	}
	if strings.Count(tracker.Reasoning(), "RAHASIA-P12") != 1 {
		t.Fatalf("the same reasoning block was stored more than once: %d", strings.Count(tracker.Reasoning(), "RAHASIA-P12"))
	}
}

func TestShortThinkingBlockAndFullReasoningShareOneBlob(t *testing.T) {
	tracker := NewTracker()
	short := strings.Repeat("langkah ", 60) // the 500-char tool.progress block
	full := short + " dan sisanya yang hanya muncul di payload terminal."
	at := time.Now()
	tracker.Observe(hermes.Event{Kind: "thinking", Text: short, TS: 1}, at)
	tracker.Observe(hermes.Event{Kind: "done", Reasoning: full, TS: 2}, at)
	got := tracker.Reasoning()
	if strings.Count(got, "langkah ") != 60 {
		t.Fatalf("thinking block was duplicated: %d occurrences", strings.Count(got, "langkah "))
	}
	if !strings.HasSuffix(got, "payload terminal.") {
		t.Fatalf("full reasoning tail is missing: %q", got)
	}
}

func TestApprovalIsAStatusStepThatEndsWhenWorkResumes(t *testing.T) {
	tracker := NewTracker()
	at := time.Now()
	tracker.Observe(hermes.Event{Kind: "approval", Name: "terminal", TS: 20}, at)
	tracker.Observe(hermes.Event{Kind: "delta", Text: "lanjut", TS: 32}, at)
	steps := tracker.Steps()
	if len(steps) != 1 || steps[0].Kind != KindStatus || steps[0].Status != StatusOK {
		t.Fatalf("approval step = %+v", steps)
	}
	if steps[0].DurationMs != 12000 {
		t.Fatalf("approval duration = %d, want 12000", steps[0].DurationMs)
	}
}
