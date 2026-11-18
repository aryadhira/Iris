package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aryadhira/iris/internal/artifacts"
	"github.com/aryadhira/iris/internal/continuation"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/live"
	"github.com/aryadhira/iris/internal/reply"
	"github.com/aryadhira/iris/internal/rooms"
	"github.com/aryadhira/iris/internal/store"
	"github.com/aryadhira/iris/internal/turntrace"
)

// turnOutcome is everything a finished turn hands to persistence and to the
// continuation decision. Nothing here is inferred from what the agent claims;
// tools and process ids come from the stream itself.
type turnOutcome struct {
	rawBody string
	runID   string
	meta    continuation.Meta
	stopped bool
	usage   *hermes.Usage
	runtime hermes.Runtime
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
// nil (a continuation has no browser attached). onVisible receives the whole
// cleaned text so the caller can persist a partial answer; onRunID receives the
// id as soon as Hermes names it, so a crash leaves the row linked to its run.
func (s *Server) runTurn(
	ctx context.Context,
	room store.Room,
	prompt string,
	emit func(name string, payload any),
	onVisible func(text string),
	onRunID func(runID string),
) (turnOutcome, error) {
	var (
		assistant     strings.Builder
		visibleText   strings.Builder
		finalText     string
		runID         string
		stopped       bool
		completedSeen bool
		usage         *hermes.Usage
		runtime       hermes.Runtime
	)
	trace := newToolTrace()
	steps := turntrace.NewTracker()
	currentTool := ""
	// The live stream is cleaned as it arrives. The parser still sees the raw
	// text below, so a STATUS line is never lost to the pump just because the
	// screen is not allowed to show it.
	visible := &reply.Stream{}

	streamErr := s.hermes.StreamChat(ctx, room.HermesSessionID, prompt, rooms.TurnInstructions(room.SystemPrompt), func(ev hermes.Event) error {
		if ev.RunID != "" && ev.RunID != runID {
			runID = ev.RunID
			s.upsertRun(ctx, room, runID, ev.SessionID, "running")
			if onRunID != nil {
				onRunID(runID)
			}
		}
		at := s.now()
		trace.observe(ev, at)
		for _, step := range steps.Observe(ev, at) {
			sendEvent(emit, "step", step)
		}
		switch ev.Kind {
		case "delta":
			assistant.WriteString(ev.Text)
			if text := visible.Feed(ev.Text); text != "" {
				visibleText.WriteString(text)
				sendEvent(emit, "delta", map[string]string{"text": text})
				if onVisible != nil {
					onVisible(visibleText.String())
				}
			}
		case "tool":
			if ev.Name != "" && (ev.Status == "started" || ev.Status == "" || ev.Status == "running") {
				currentTool = ev.Name
			}
			name := ev.Name
			if name == "" {
				name = "tool"
			}
			status := ev.Status
			if status == "" {
				status = "running"
			}
			sendEvent(emit, "tool", map[string]string{
				"name":   name,
				"status": status,
				"detail": turntrace.ToolDetail(ev.Preview),
			})
		case "approval":
			toolName := ev.Name
			if toolName == "" {
				// The session-stream approval payload carries the command and
				// description but no tool name. The tool.started that caused it
				// is real stream data, so use that rather than inventing one.
				toolName = currentTool
			}
			ev.Name = toolName
			s.updateRunStatus(ctx, runID, "waiting_for_approval", false)
			if row, err := s.recordApproval(ctx, room, runID, ev, room.HermesSessionID); err != nil {
				s.logger.Error("cannot persist approval", "error", err, "room_id", room.ID.String(), "run_id", runID)
			} else {
				s.hub.Publish(room.ID, live.Event{Name: "approval", Payload: viewApproval(*row)})
				s.notifyApproval(ctx, room, *row)
			}
		case "done":
			if ev.Text != "" {
				finalText = ev.Text
			}
			// The terminal payload is the meter. Keep the last non-nil usage:
			// the bare `done` sentinel that follows run.completed has none and
			// must not erase what the real terminal event reported.
			if ev.Usage != nil {
				usage = ev.Usage
			}
			if ev.Runtime.Provider != "" || ev.Runtime.Model != "" {
				runtime = ev.Runtime
			}
			// The bare ``done`` sentinel closes the stream after run.cancelled;
			// it must not overwrite a stop decision with "completed".
			if ev.OriginalKind == "run.completed" || ev.OriginalKind == "assistant.completed" || ev.OriginalKind == "" {
				completedSeen = true
			}
		case "cancelled":
			stopped = true
			finalText = ""
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

	// Release any held tail (an ordinary final line, or a line whose status was
	// withheld). Flushing is safe after a stop as well: the raw body is kept.
	if text := visible.Flush(); text != "" {
		visibleText.WriteString(text)
		sendEvent(emit, "delta", map[string]string{"text": text})
		if onVisible != nil {
			onVisible(visibleText.String())
		}
	}

	rawBody := assistant.String()
	if rawBody == "" && finalText != "" {
		rawBody = finalText
	}

	trace.procs = continuation.AppendProcs(trace.procs, continuation.ExtractProcs(rawBody)...)
	trace.procs = continuation.AppendProcs(trace.procs, continuation.ExtractProcs(finalText)...)
	trace.finish(s.now())
	for _, step := range steps.Finish(s.now()) {
		sendEvent(emit, "step", step)
	}

	_, statusLine := continuation.ParseStatus(rawBody)
	if statusLine == "" && finalText != "" {
		_, statusLine = continuation.ParseStatus(finalText)
	}

	switch {
	case stopped:
		s.updateRunStatus(ctx, runID, "cancelled", true)
	case streamErr != nil:
		s.finishRunUnlessStopping(ctx, runID)
	case completedSeen:
		s.updateRunStatus(ctx, runID, "completed", true)
	default:
		s.finishRunUnlessStopping(ctx, runID)
	}

	return turnOutcome{
		rawBody: rawBody,
		runID:   runID,
		stopped: stopped,
		usage:   usage,
		runtime: runtime,
		meta: continuation.Meta{
			Procs:      trace.procs,
			Tools:      trace.tools,
			Steps:      steps.Steps(),
			StatusLine: statusLine,
			Reasoning:  steps.Reasoning(),
		},
	}, streamErr
}

func (s *Server) upsertRun(ctx context.Context, room store.Room, runID, sessionID, status string) {
	if runID == "" {
		return
	}
	if sessionID == "" {
		sessionID = room.HermesSessionID
	}
	now := s.now()
	if _, err := s.store.UpsertRun(ctx, store.UpsertRunParams{
		RunID:     runID,
		RoomID:    room.ID,
		SessionID: sessionID,
		Status:    status,
		StartedAt: now,
	}); err != nil {
		s.logger.Error("cannot record run", "error", err, "run_id", runID, "room_id", room.ID.String())
	}
}

func (s *Server) updateRunStatus(ctx context.Context, runID, status string, finished bool) {
	if runID == "" {
		return
	}
	now := s.now()
	params := store.UpdateRunStatusParams{
		RunID:     runID,
		Status:    status,
		UpdatedAt: now,
	}
	if finished {
		params.FinishedAt = &now
	}
	if _, err := s.store.UpdateRunStatus(ctx, params); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("cannot update run status", "error", err, "run_id", runID, "status", status)
	}
}

// finishRunUnlessStopping never overwrites a stop decision with "failed": the
// gateway's cancelled event may arrive after the stream's read loop returns.
func (s *Server) finishRunUnlessStopping(ctx context.Context, runID string) {
	if runID == "" {
		return
	}
	row, err := s.store.GetRun(ctx, runID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Error("cannot read run before failure", "error", err, "run_id", runID)
		}
		return
	}
	if row.Status == "stopping" || row.Status == "cancelled" || row.Status == "completed" {
		return
	}
	s.updateRunStatus(ctx, runID, "failed", true)
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
		return
	}
	s.publishRoomEvent(roomID, "note", map[string]string{"body": body})
}

// partialWriter throttles writes of the streaming body. It never holds a turn
// up: a failed partial write is logged and the next delta tries again.
type partialWriter struct {
	s       *Server
	ctx     context.Context
	id      uuid.UUID
	lastLen int
	lastAt  time.Time
}

func (s *Server) newPartialWriter(ctx context.Context, id uuid.UUID) *partialWriter {
	return &partialWriter{s: s, ctx: ctx, id: id, lastAt: s.now()}
}

func (w *partialWriter) update(text string) {
	if w == nil || w.id == uuid.Nil {
		return
	}
	if len(text)-w.lastLen < w.s.partialChars && w.s.now().Sub(w.lastAt) < w.s.partialInterval {
		return
	}
	w.write(text)
}

func (w *partialWriter) write(text string) {
	w.lastLen = len(text)
	w.lastAt = w.s.now()
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(w.ctx), persistTimeout)
	defer cancel()
	if err := w.s.store.UpdateMessagePartial(writeCtx, store.UpdateMessagePartialParams{
		ID:   w.id,
		Body: text,
	}); err != nil {
		w.s.logger.Error("cannot persist partial reply", "error", err, "message_id", w.id.String())
	}
}

// startAssistant creates the row before the first token, so a reload during
// the turn has somewhere to read from.
func (s *Server) startAssistant(ctx context.Context, room store.Room) (*store.Message, error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	row, err := s.store.StartStreamingMessage(writeCtx, store.StartStreamingMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Meta:   []byte("{}"),
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Server) setAssistantRunID(ctx context.Context, id uuid.UUID, runID string) {
	if id == uuid.Nil || runID == "" {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if err := s.store.SetMessageRunID(writeCtx, store.SetMessageRunIDParams{
		ID:    id,
		RunID: optionalString(runID),
	}); err != nil {
		s.logger.Error("cannot link streaming message to run", "error", err, "message_id", id.String())
	}
}

func (s *Server) discardAssistant(ctx context.Context, id uuid.UUID) {
	if id == uuid.Nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if err := s.store.DeleteStreamingMessage(writeCtx, id); err != nil {
		s.logger.Error("cannot discard empty streaming message", "error", err, "message_id", id.String())
	}
}

// finalizeAssistant strips valid artifact markers, stores the visible text with
// its meta, and links any real artifact files. A missing file keeps its marker
// in the body, visibly. state is done for a finished turn and interrupted for a
// stream that died without a terminal event.
func (s *Server) finalizeAssistant(
	ctx context.Context,
	room store.Room,
	messageID uuid.UUID,
	rawBody string,
	runID string,
	meta continuation.Meta,
	state string,
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
		// artifacts.Process is the one place that decides whether a marker is a
		// real file or a broken promise; the cleaner must not hide the latter.
		cleanedBody = reply.CleanKeepingArtifacts(result.Body)
		found = result.Files
		for _, broken := range result.Broken {
			s.logger.Warn("artifact promised but not found", "room_id", room.ID.String(), "path", broken)
		}
	}

	metaBytes, err := json.Marshal(meta)
	if err != nil {
		metaBytes = []byte("{}")
	}
	if state == "" {
		state = "done"
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	row, err := s.store.FinalizeMessage(persistCtx, store.FinalizeMessageParams{
		ID:    messageID,
		Body:  cleanedBody,
		RunID: optionalString(runID),
		Meta:  metaBytes,
		State: state,
	})
	if err != nil {
		return nil, err
	}
	for _, item := range found {
		s.saveArtifact(room, row.ID, item)
	}
	return &row, nil
}

func (s *Server) runIsStopped(ctx context.Context, runID string) bool {
	if runID == "" {
		return false
	}
	row, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	return row.Status == "stopping" || row.Status == "cancelled"
}
