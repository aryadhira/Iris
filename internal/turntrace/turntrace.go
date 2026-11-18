// Package turntrace turns the Hermes stream into the steps a human can see:
// thinking, tools, plans and status. It is deliberately pure — no database and
// no clock — so every ordering and duration rule is cheap to test.
//
// A step is the visible working log of one turn. It never becomes the reply
// body: the reasoning excerpt lives beside it, capped and private.
package turntrace

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aryadhira/iris/internal/hermes"
)

// Step kinds. Only kinds Hermes actually emits are produced; a plan step is
// never invented for a turn that did not report one.
const (
	KindThinking = "thinking"
	KindTool     = "tool"
	KindPlan     = "plan"
	KindStatus   = "status"
)

// Step statuses.
const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusFailed  = "failed"
)

// MaxReasoning is the one bounded reasoning blob stored on a message. It is
// private working text: never the body, never search, never export, never TTS.
const MaxReasoning = 4 << 10

const (
	maxDetail     = 200
	maxToolDetail = 120
)

// Step is one visible event in a turn. Timestamps are the gateway's own when
// it sent one, otherwise irisd's clock; duration is only filled when the step
// actually ended.
type Step struct {
	Seq        int        `json:"seq"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
	DurationMs int64      `json:"durationMs"`
}

// Tracker accumulates steps in arrival order. It is used by one turn at a
// time, so it needs no lock.
type Tracker struct {
	steps []Step

	openTool     map[string][]int
	openThinking int
	openStatus   int

	reasoning string
}

// NewTracker returns an empty log.
func NewTracker() *Tracker {
	return &Tracker{openTool: make(map[string][]int), openThinking: -1, openStatus: -1}
}

// ToolDetail is the only tool text Iris forwards: one line, short, redacted.
// Raw args are never used.
func ToolDetail(preview string) string {
	return shortDetail(redact(preview), maxToolDetail)
}

// Steps returns a copy, oldest first.
func (t *Tracker) Steps() []Step {
	out := make([]Step, len(t.steps))
	copy(out, t.steps)
	return out
}

// Reasoning returns the bounded private excerpt (≤4 KB).
func (t *Tracker) Reasoning() string {
	return t.reasoning
}

// Observe consumes one normalized Hermes event and returns every step that was
// added or changed, so the caller can publish exactly those to the room.
func (t *Tracker) Observe(ev hermes.Event, at time.Time) []Step {
	ts := eventTime(ev, at)
	switch ev.Kind {
	case "thinking":
		changed := t.closeStatus(ts)
		t.appendReasoning(ev.Text)
		if t.openThinking >= 0 {
			step := &t.steps[t.openThinking]
			combined := strings.TrimSpace(step.Detail + " " + ev.Text)
			step.Detail = shortDetail(combined, maxDetail)
			step.EndedAt = nil
			step.DurationMs = 0
			return append(changed, *step)
		}
		t.openThinking = t.add(Step{
			Kind:      KindThinking,
			Name:      "berpikir",
			Detail:    shortDetail(ev.Text, maxDetail),
			Status:    StatusRunning,
			StartedAt: ts,
		})
		return append(changed, t.steps[t.openThinking])

	case "tool":
		changed := t.closeThinking(ts)
		changed = append(changed, t.closeStatus(ts)...)
		name := strings.TrimSpace(ev.Name)
		if name == "" || strings.HasPrefix(name, "_") {
			return changed
		}
		detail := ToolDetail(ev.Preview)
		switch ev.Status {
		case "completed", "failed", "error":
			status := StatusOK
			if ev.Status == "failed" || ev.Status == "error" {
				status = StatusFailed
			}
			indexes := t.openTool[strings.ToLower(name)]
			if len(indexes) > 0 {
				index := indexes[len(indexes)-1]
				t.openTool[strings.ToLower(name)] = indexes[:len(indexes)-1]
				step := &t.steps[index]
				step.Status = status
				step.EndedAt = timePtr(ts)
				step.DurationMs = durationMs(step.StartedAt, ts)
				if detail != "" && status == StatusFailed {
					step.Detail = detail
				}
				return append(changed, *step)
			}
			// A completion without a start is still a real completion. It has
			// no duration to claim, so it is stored with zero, not invented.
			ended := ts
			t.add(Step{
				Kind:      KindTool,
				Name:      name,
				Detail:    detail,
				Status:    status,
				StartedAt: ts,
				EndedAt:   &ended,
			})
			return append(changed, t.steps[len(t.steps)-1])
		default:
			indexes := t.openTool[strings.ToLower(name)]
			if len(indexes) > 0 {
				index := indexes[len(indexes)-1]
				step := &t.steps[index]
				if detail != "" {
					step.Detail = detail
				}
				return append(changed, *step)
			}
			t.add(Step{
				Kind:      KindTool,
				Name:      name,
				Detail:    detail,
				Status:    StatusRunning,
				StartedAt: ts,
			})
			t.openTool[strings.ToLower(name)] = append(indexes, len(t.steps)-1)
			return append(changed, t.steps[len(t.steps)-1])
		}

	case "approval":
		changed := t.closeThinking(ts)
		changed = append(changed, t.closeStatus(ts)...)
		name := "menunggu izin"
		detail := shortDetail(redact(ev.Name), maxToolDetail)
		t.openStatus = t.add(Step{
			Kind:      KindStatus,
			Name:      name,
			Detail:    detail,
			Status:    StatusRunning,
			StartedAt: ts,
		})
		return append(changed, t.steps[t.openStatus])

	case "delta", "done", "cancelled", "error":
		changed := t.closeThinking(ts)
		changed = append(changed, t.closeStatus(ts)...)
		if ev.Kind == "done" && ev.Reasoning != "" {
			t.appendReasoning(ev.Reasoning)
		}
		return changed
	}
	return nil
}

// Finish closes every still-open step at the turn's end with a real duration.
// A tool that never reported completion keeps its running status: Iris knows
// the turn ended, not that the command succeeded.
func (t *Tracker) Finish(at time.Time) []Step {
	changed := t.closeThinking(at)
	changed = append(changed, t.closeStatus(at)...)
	for key, indexes := range t.openTool {
		for _, index := range indexes {
			step := &t.steps[index]
			step.EndedAt = timePtr(at)
			step.DurationMs = durationMs(step.StartedAt, at)
			changed = append(changed, *step)
		}
		delete(t.openTool, key)
	}
	return changed
}

func (t *Tracker) add(step Step) int {
	step.Seq = len(t.steps) + 1
	t.steps = append(t.steps, step)
	return len(t.steps) - 1
}

func (t *Tracker) closeThinking(ts time.Time) []Step {
	if t.openThinking < 0 {
		return nil
	}
	step := &t.steps[t.openThinking]
	step.Status = StatusOK
	step.EndedAt = timePtr(ts)
	step.DurationMs = durationMs(step.StartedAt, ts)
	t.openThinking = -1
	return []Step{*step}
}

func (t *Tracker) closeStatus(ts time.Time) []Step {
	if t.openStatus < 0 {
		return nil
	}
	step := &t.steps[t.openStatus]
	step.Status = StatusOK
	step.EndedAt = timePtr(ts)
	step.DurationMs = durationMs(step.StartedAt, ts)
	t.openStatus = -1
	return []Step{*step}
}

func (t *Tracker) appendReasoning(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if strings.Contains(t.reasoning, text) {
		return
	}
	// Hermes first sends a short `_thinking` block and later the full reasoning
	// on the terminal payload. Overlapping the suffix with the new prefix keeps
	// one blob instead of storing the same block twice.
	overlap := suffixPrefixOverlap(t.reasoning, text)
	text = text[overlap:]
	if text == "" {
		return
	}
	sep := ""
	if t.reasoning != "" && overlap == 0 {
		sep = "\n\n"
	}
	remaining := MaxReasoning - len(t.reasoning) - len(sep)
	if remaining <= 0 {
		return
	}
	if len(text) > remaining {
		cut := remaining
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	t.reasoning += sep + text
}

// suffixPrefixOverlap is the length of the longest suffix of current that is
// also a prefix of next. It is how a streamed thinking block and its completed
// copy are stitched into one blob.
func suffixPrefixOverlap(current, next string) int {
	limit := len(current)
	if len(next) < limit {
		limit = len(next)
	}
	for n := limit; n > 0; n-- {
		if strings.HasSuffix(current, next[:n]) {
			return n
		}
	}
	return 0
}

func eventTime(ev hermes.Event, at time.Time) time.Time {
	if ev.TS > 0 {
		sec := int64(ev.TS)
		nsec := int64((ev.TS - float64(sec)) * 1e9)
		return time.Unix(sec, nsec).UTC()
	}
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}

func durationMs(start, end time.Time) int64 {
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func timePtr(t time.Time) *time.Time {
	return &t
}

var (
	bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	skPattern     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
	secretPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|passwd)\b\s*[=:]\s*("[^"]*"|'[^']*'|\S+)`)
)

// redact keeps a tool preview short and removes the obvious secret shapes
// before it is stored or sent to a phone. Hermes already redacts approvals;
// a tool preview comes from args, so it is redacted again here.
func redact(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = bearerPattern.ReplaceAllString(text, "$1[redacted]")
	text = skPattern.ReplaceAllString(text, "sk-[redacted]")
	text = secretPattern.ReplaceAllString(text, "$1=[redacted]")
	return strings.Join(strings.Fields(text), " ")
}

func shortDetail(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return ""
	}
	return truncateRunes(text, limit)
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut]) + "…"
}
