// Package continuation holds the pure decisions of the continuity pump: what a
// turn left running, whether it needs a follow-up, and which bound stops it.
// No database and no clock live here, so every rule has a cheap test.
package continuation

import (
	"regexp"
	"strings"
	"time"

	"github.com/aryadhira/iris/internal/turntrace"
)

const (
	// MaxPerChain is the hard ceiling on automatic follow-ups.
	MaxPerChain = 3
	// Window is the wall-clock lifetime of one chain, counted from its first
	// assistant message.
	Window = 30 * time.Minute
	// Delay is how long irisd waits before the first follow-up.
	Delay = 20 * time.Second
)

// Tool is one visible tool call in a turn, oldest first.
type Tool struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"durationMs"`
	Status     string `json:"status"`
}

// Meta is stored on the assistant message row.
type Meta struct {
	Procs      []string         `json:"procs,omitempty"`
	Tools      []Tool           `json:"tools,omitempty"`
	Steps      []turntrace.Step `json:"steps,omitempty"`
	StatusLine string           `json:"statusLine,omitempty"`
	// Reasoning is the capped private working text. It is stored here, never in
	// the message body, so search, export and speech cannot reach it.
	Reasoning string `json:"reasoning,omitempty"`
}

// Empty reports whether the row has nothing worth showing. An empty timeline
// must render nothing, never a placeholder row.
func (m Meta) Empty() bool {
	return len(m.Procs) == 0 && len(m.Tools) == 0 && len(m.Steps) == 0 &&
		m.StatusLine == "" && m.Reasoning == ""
}

// Status is the agent's own final line. It is a claim, not proof.
type Status string

const (
	StatusNone    Status = ""
	StatusRunning Status = "RUNNING"
	StatusDone    Status = "DONE"
	StatusBlocked Status = "BLOCKED"
)

var (
	procPattern   = regexp.MustCompile(`(?i)\bproc_[0-9a-z]+\b`)
	statusPattern = regexp.MustCompile(`(?im)^[\s>*_` + "`" + `]*STATUS:\s*(RUNNING|DONE|BLOCKED|CONTINUE)\b[^\n]*`)
)

// ExtractProcs returns every distinct process id in the given text, in the
// order it first appeared. Case is preserved so a log matches what Hermes said.
func ExtractProcs(texts ...string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, text := range texts {
		for _, match := range procPattern.FindAllString(text, -1) {
			key := strings.ToLower(match)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, match)
		}
	}
	return out
}

// AppendProcs merges extra ids into an existing list without duplicates.
func AppendProcs(existing []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, id := range existing {
		seen[strings.ToLower(id)] = struct{}{}
	}
	for _, id := range extra {
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, id)
	}
	return existing
}

// ParseStatus finds the last STATUS line in a reply. The contract asks for it
// at the end; taking the last one means an example earlier in a long answer
// cannot win. The returned line is the cleaned display text.
func ParseStatus(body string) (Status, string) {
	matches := statusPattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return StatusNone, ""
	}
	match := matches[len(matches)-1]
	line := cleanLine(match[0])
	kind := strings.ToUpper(match[1])
	// CONTINUE is a synonym for "still working" in the contract vocabulary.
	// It must keep the pump alive, not stop it because the word is unfamiliar.
	if kind == "CONTINUE" {
		kind = string(StatusRunning)
	}
	return Status(kind), line
}

func cleanLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "*_`> \t")
	line = strings.TrimSpace(line)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return line
}

// StatusKind normalises a stored status line for the UI.
func StatusKind(line string) Status {
	matches := statusPattern.FindStringSubmatch(line)
	if len(matches) < 2 {
		return StatusNone
	}
	kind := strings.ToUpper(matches[1])
	if kind == "CONTINUE" {
		return StatusRunning
	}
	return Status(kind)
}

// Action says what the pump should do after a turn.
type Action struct {
	Kind   string // schedule, none, skip, capped, cancel
	Reason string
}

const (
	ActionSchedule = "schedule"
	ActionNone     = "none"
	ActionSkip     = "skip"
	ActionCapped   = "capped"
	ActionCancel   = "cancel"
)

// Input is everything the decision needs, already read from the database (or a
// fake clock in tests).
type Input struct {
	Status           Status
	Procs            []string
	ToolCount        int
	IsContinuation   bool
	Attempt          int // how many continuations were already scheduled
	Now              time.Time
	Deadline         time.Time
	Archived         bool
	NewerUserMessage bool
}

// Decide applies the bounds in the order that matters to a human: a cancelled
// room over a capped one, a finished job over a stale process id.
func Decide(in Input) Action {
	if in.Archived {
		return Action{Kind: ActionCancel, Reason: "room diarsipkan"}
	}
	if in.NewerUserMessage {
		return Action{Kind: ActionCancel, Reason: "ada pesan baru dari Boss"}
	}
	if in.Status == StatusDone || in.Status == StatusBlocked {
		return Action{Kind: ActionNone, Reason: "turn selesai dengan STATUS: " + string(in.Status)}
	}
	if in.IsContinuation && in.ToolCount == 0 {
		return Action{Kind: ActionSkip, Reason: "lanjutan sebelumnya tidak memakai alat apa pun"}
	}
	if in.Status != StatusRunning && len(in.Procs) == 0 {
		return Action{Kind: ActionNone, Reason: "tidak ada proses yang perlu ditunggu"}
	}
	if in.Attempt >= MaxPerChain {
		return Action{Kind: ActionCapped, Reason: "batas 3 lanjutan otomatis tercapai"}
	}
	if !in.Now.Before(in.Deadline) {
		return Action{Kind: ActionCapped, Reason: "batas 30 menit tercapai"}
	}
	return Action{Kind: ActionSchedule, Reason: "turn meninggalkan proses yang masih berjalan"}
}
