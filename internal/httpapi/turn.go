package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/artifacts"
	"github.com/aryadhira/iris/internal/continuation"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/rooms"
	"github.com/aryadhira/iris/internal/store"
)

// turnOutcome is everything a finished turn hands to persistence and to the
// continuation decision. Nothing here is inferred from what the agent claims;
// tools and process ids come from the stream itself.
type turnOutcome struct {
	rawBody string
	runID   string
	meta    continuation.Meta
}

type toolTrace struct {
	tools  []continuation.Tool
	starts []float64
	open   map[string][]int
	procs  []string
}

func newToolTrace() *toolTrace {
	return &toolTrace{open: make(map[string][]int)}
}

// observe records one tool event. Internal tools (names beginning with "_",
// e.g. _thinking) are not work Boss can see, so they never become a timeline
// row and never count as "the continuation did something".
func (t *toolTrace) observe(ev hermes.Event, at time.Time) {
	if ev.Kind == "tool" || ev.Kind == "done" {
		t.procs = continuation.AppendProcs(t.procs, continuation.ExtractProcs(string(ev.Raw))...)
	}
	if ev.Kind != "tool" {
		return
	}
	name := strings.TrimSpace(ev.Name)
	if name == "" || strings.HasPrefix(name, "_") {
		return
	}
	ts := ev.TS
	if ts <= 0 {
		ts = float64(at.UnixNano()) / float64(time.Second)
	}
	key := strings.ToLower(name)

	switch ev.Status {
	case "completed", "failed", "error":
		indexes := t.open[key]
		status := "completed"
		if ev.Status == "failed" || ev.Status == "error" {
			status = "failed"
		}
		if len(indexes) > 0 {
			index := indexes[len(indexes)-1]
			t.open[key] = indexes[:len(indexes)-1]
			t.tools[index].Status = status
			t.tools[index].DurationMs = durationMs(t.starts[index], ts)
			return
		}
		t.tools = append(t.tools, continuation.Tool{Name: name, Status: status})
		t.starts = append(t.starts, ts)
	case "started":
		t.tools = append(t.tools, continuation.Tool{Name: name, Status: "running"})
		t.starts = append(t.starts, ts)
		t.open[key] = append(t.open[key], len(t.tools)-1)
	}
}

// finish closes tools that never reported completion, so the UI shows a real
// duration instead of an invented "done".
func (t *toolTrace) finish(at time.Time) {
	ts := float64(at.UnixNano()) / float64(time.Second)
	for _, indexes := range t.open {
		for _, index := range indexes {
			t.tools[index].DurationMs = durationMs(t.starts[index], ts)
		}
	}
	t.open = make(map[string][]int)
}

func durationMs(start, end float64) int64 {
	if start <= 0 || end < start {
		return 0
	}
	return int64((end - start) * 1000)
}

// runTurn streams one Hermes turn and returns the honest trace. emit may be
// nil (a continuation has no browser attached).
func (s *Server) runTurn(
	ctx context.Context,
	room store.Room,
	prompt string,
	emit func(name string, payload any),
) (turnOutcome, error) {
	var (
		assistant strings.Builder
		finalText string
		runID     string
	)
	trace := newToolTrace()

	streamErr := s.hermes.StreamChat(ctx, room.HermesSessionID, prompt, rooms.TurnInstructions(room.SystemPrompt), func(ev hermes.Event) error {
		if ev.RunID != "" {
			runID = ev.RunID
		}
		trace.observe(ev, s.now())
		switch ev.Kind {
		case "delta":
			assistant.WriteString(ev.Text)
			sendEvent(emit, "delta", map[string]string{"text": ev.Text})
		case "tool":
			name := ev.Name
			if name == "" {
				name = "tool"
			}
			status := ev.Status
			if status == "" {
				status = "running"
			}
			sendEvent(emit, "tool", map[string]string{"name": name, "status": status})
		case "done":
			if ev.Text != "" {
				finalText = ev.Text
			}
		case "error":
			message := ev.Text
			if message == "" {
				message = "Hermes reported a problem with this turn."
			}
			sendEvent(emit, "error", map[string]string{"message": message})
		case "unknown":
			raw := any(string(ev.Raw))
			if json.Valid(ev.Raw) {
				raw = json.RawMessage(ev.Raw)
			}
			sendEvent(emit, "unknown", map[string]any{"kind": ev.OriginalKind, "raw": raw})
		}
		return nil
	})

	rawBody := assistant.String()
	if rawBody == "" && finalText != "" {
		rawBody = finalText
	}

	trace.procs = continuation.AppendProcs(trace.procs, continuation.ExtractProcs(rawBody)...)
	trace.procs = continuation.AppendProcs(trace.procs, continuation.ExtractProcs(finalText)...)
	trace.finish(s.now())

	_, statusLine := continuation.ParseStatus(rawBody)
	if statusLine == "" && finalText != "" {
		_, statusLine = continuation.ParseStatus(finalText)
	}

	return turnOutcome{
		rawBody: rawBody,
		runID:   runID,
		meta: continuation.Meta{
			Procs:      trace.procs,
			Tools:      trace.tools,
			StatusLine: statusLine,
		},
	}, streamErr
}

func sendEvent(emit func(name string, payload any), name string, payload any) {
	if emit != nil {
		emit(name, payload)
	}
}

// noteSystem writes a visible line in the room timeline. The pump's skip,
// cancel and cap reasons must be readable without opening a log file.
func (s *Server) noteSystem(ctx context.Context, roomID uuid.UUID, body string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if _, err := s.store.InsertMessage(writeCtx, store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: roomID,
		Role:   "system",
		Body:   body,
		Meta:   []byte("{}"),
	}); err != nil {
		s.logger.Error("cannot write system line", "error", err, "room_id", roomID.String())
	}
}

// persistAssistant strips valid artifact markers, stores the visible text with
// its meta, and links any real artifact files. A missing file keeps its marker
// in the body, visibly.
func (s *Server) persistAssistant(
	ctx context.Context,
	room store.Room,
	rawBody string,
	runID string,
	meta continuation.Meta,
) (*store.Message, error) {
	cleanedBody := rawBody
	var found []artifacts.Found
	if rawBody != "" {
		folder := rooms.Open(room.FolderPath)
		result := artifacts.Process(rawBody, func(rel string) (string, error) {
			abs, err := folder.Resolve(rel)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(abs)
			if err != nil {
				return "", err
			}
			if !info.Mode().IsRegular() {
				return "", errors.New("not a regular file")
			}
			return abs, nil
		})
		cleanedBody = result.Body
		found = result.Files
		for _, broken := range result.Broken {
			s.logger.Warn("artifact promised but not found", "room_id", room.ID.String(), "path", broken)
		}
	}

	metaBytes, err := json.Marshal(meta)
	if err != nil {
		metaBytes = []byte("{}")
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	row, err := s.store.InsertMessage(persistCtx, store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Role:   "assistant",
		Body:   cleanedBody,
		RunID:  optionalString(runID),
		Meta:   metaBytes,
	})
	if err != nil {
		return nil, err
	}
	for _, item := range found {
		s.saveArtifact(room, row.ID, item)
	}
	return &row, nil
}
