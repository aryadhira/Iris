package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/live"
	"github.com/aryadhira/iris/internal/store"
)

const (
	liveHeartbeat = 15 * time.Second
	// A snapshot never needs every historical approval. The transcript pulls
	// its own first page; this is enough to render a late joiner's cards.
	snapshotApprovalLimit = 50
)

// roomSnapshot is the first frame a client receives when it attaches. It is
// built after subscribing, so the in-memory partial is exactly the text before
// the first delta this subscriber will see.
type roomSnapshot struct {
	Run          *runView       `json:"run"`
	Partial      string         `json:"partial"`
	Approvals    []approvalView `json:"approvals"`
	RemindersDue int            `json:"reminders_due"`
}

// roomEvents is the room's live stream: a snapshot, then deltas, tools,
// approvals, notes and one done. It needs no replay log because the snapshot
// carries the only state a late joiner needs.
func (s *Server) roomEvents(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if _, err := s.store.GetRoom(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for events", "error", err, "room_id", id.String())
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room ini."))
	}

	s.expireApprovals(ctx)
	snapshot, events, unsubscribe := s.hub.Subscribe(id)
	defer unsubscribe()

	// The hub is the live text. A just-created row is the fallback for the
	// moment between the first persist and the first delta on this process.
	partial := snapshot.Partial
	if partial == "" {
		if row, err := s.store.GetStreamingMessage(ctx, id); err == nil {
			partial = row.Body
		}
	}

	var run *store.Run
	if row, err := s.store.GetLatestRun(ctx, id); err == nil {
		run = activeRun(&row)
	}

	approvals := make([]approvalView, 0)
	if rows, err := s.store.ListApprovals(ctx, id); err == nil {
		start := 0
		if len(rows) > snapshotApprovalLimit {
			start = len(rows) - snapshotApprovalLimit
		}
		for _, row := range rows[start:] {
			approvals = append(approvals, viewApproval(row))
		}
	}

	remindersDue := 0
	if rows, err := s.store.ListPendingReminders(ctx, id); err == nil {
		now := s.now()
		for _, row := range rows {
			if !row.DueAt.After(now) {
				remindersDue++
			}
		}
	}

	headers := c.Response().Header()
	headers.Set(echo.HeaderContentType, "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	c.Response().WriteHeader(http.StatusOK)
	c.Response().Flush()

	write := func(name string, payload any) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			s.logger.Error("cannot encode live payload", "error", err, "room_id", id.String())
			return true
		}
		if _, err := fmt.Fprintf(c.Response(), "event: %s\ndata: %s\n\n", name, body); err != nil {
			return false
		}
		c.Response().Flush()
		return true
	}

	if !write("snapshot", roomSnapshot{
		Run:          runViewOf(run),
		Partial:      partial,
		Approvals:    approvals,
		RemindersDue: remindersDue,
	}) {
		return nil
	}

	heartbeat := time.NewTicker(liveHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.C:
			// A comment keeps phones and proxies from silently dropping an idle
			// stream. It is not an event and never reaches the transcript.
			if _, err := fmt.Fprint(c.Response(), ": keep-alive\n\n"); err != nil {
				return nil
			}
			c.Response().Flush()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if !write(ev.Name, ev.Payload) {
				return nil
			}
		}
	}
}

// recoverInterrupted runs once at boot. It never deletes a partial answer and
// never claims it finished: the row becomes interrupted and the room gets one
// visible note per affected turn.
func (s *Server) recoverInterrupted(ctx context.Context) {
	rows, err := s.store.MarkStreamingMessagesInterrupted(ctx)
	if err != nil {
		s.logger.Error("cannot mark interrupted messages", "error", err)
		return
	}
	now := s.now()
	runs, runErr := s.store.MarkActiveRunsInterrupted(ctx, now)
	if runErr != nil {
		s.logger.Error("cannot mark interrupted runs", "error", runErr)
	}
	if len(rows) == 0 && (runErr != nil || len(runs) == 0) {
		return
	}
	noted := make(map[uuid.UUID]struct{})
	for _, row := range rows {
		noted[row.RoomID] = struct{}{}
	}
	for _, run := range runs {
		noted[run.RoomID] = struct{}{}
	}
	for roomID := range noted {
		s.logger.Warn("turn interrupted by restart", "room_id", roomID.String())
		s.noteSystem(ctx, roomID, "balasan sebelumnya terputus — Iris dimulai ulang di tengah giliran.")
	}
}

// activeRun returns the run only while it is live or visibly interrupted. A
// completed run is history; returning it as `run` would make an idle room look
// busy forever.
func activeRun(row *store.Run) *store.Run {
	if row == nil {
		return nil
	}
	switch row.Status {
	case "queued", "running", "waiting_for_approval", "stopping", "interrupted":
		return row
	default:
		return nil
	}
}

// publishRoomEvent is the one path from a turn to every attached client.
// Deltas also extend the hub's partial text atomically with the frame.
func (s *Server) publishRoomEvent(roomID uuid.UUID, name string, payload any) {
	if s.hub == nil {
		return
	}
	if name == "delta" {
		if text, ok := payload.(map[string]string); ok {
			s.hub.Append(roomID, text["text"])
			return
		}
	}
	s.hub.Publish(roomID, live.Event{Name: name, Payload: payload})
}
