package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/continuation"
	"github.com/aryadhira/iris/internal/store"
)

const continuationBatch = 5

type chainRef struct {
	id        uuid.UUID
	createdAt time.Time
	deadline  time.Time
}

// scheduleAfterTurn is the single place that decides whether a finished turn
// arms the safety net. The counters come from the database, so a restart or a
// second request cannot double them.
func (s *Server) scheduleAfterTurn(
	ctx context.Context,
	room store.Room,
	chain chainRef,
	isContinuation bool,
	meta continuation.Meta,
) {
	status := continuation.StatusNone
	if meta.StatusLine != "" {
		status = continuation.StatusKind(meta.StatusLine)
	}

	count, err := s.store.CountContinuationsForChain(ctx, chain.id)
	if err != nil {
		s.logger.Error("cannot count continuations", "error", err, "chain_id", chain.id.String())
		return
	}
	var newer bool
	if isContinuation {
		newer, err = s.store.HasNewerUserMessage(ctx, store.HasNewerUserMessageParams{
			RoomID:    room.ID,
			CreatedAt: chain.createdAt,
		})
		if err != nil {
			s.logger.Error("cannot check for a newer user message", "error", err, "room_id", room.ID.String())
			return
		}
	}

	action := continuation.Decide(continuation.Input{
		Status:           status,
		Procs:            meta.Procs,
		ToolCount:        len(meta.Tools),
		IsContinuation:   isContinuation,
		Attempt:          int(count),
		Now:              s.now(),
		Deadline:         chain.deadline,
		Archived:         room.ArchivedAt != nil,
		NewerUserMessage: newer,
	})

	switch action.Kind {
	case continuation.ActionSchedule:
		due := s.now().Add(continuation.Delay)
		if !due.Before(chain.deadline) {
			s.stopChain(ctx, room.ID, chain.id, "capped", "batas 30 menit tercapai", "lanjut otomatis berhenti — batas 30 menit tercapai.")
			return
		}
		next := int32(count) + 1
		row, err := s.store.CreateContinuation(ctx, store.CreateContinuationParams{
			ID:       uuid.New(),
			RoomID:   room.ID,
			ChainID:  chain.id,
			Attempt:  next,
			DueAt:    due,
			Deadline: chain.deadline,
		})
		if err != nil {
			s.logger.Error("cannot schedule continuation", "error", err, "room_id", room.ID.String())
			return
		}
		s.logger.Info(
			"continuation scheduled",
			"reason", action.Reason,
			"room_id", room.ID.String(),
			"chain_id", chain.id.String(),
			"attempt", row.Attempt,
			"due_at", row.DueAt.Format(time.RFC3339Nano),
			"procs", meta.Procs,
		)
	case continuation.ActionSkip, continuation.ActionCapped, continuation.ActionCancel:
		s.stopChain(ctx, room.ID, chain.id, action.Kind, action.Reason, visibleReason(action.Kind, action.Reason))
	default:
		s.logger.Debug("continuation not needed", "reason", action.Reason, "room_id", room.ID.String())
	}
}

func visibleReason(kind, reason string) string {
	switch kind {
	case "cancel":
		return "lanjut otomatis dibatalkan — " + reason + "."
	case "capped":
		return "lanjut otomatis berhenti — " + reason + "."
	default:
		return "lanjut otomatis berhenti — " + reason + "."
	}
}

// stopChain writes one honest line and logs the decision. It never invents a
// status message when there is nothing pending to stop.
func (s *Server) stopChain(ctx context.Context, roomID, chainID uuid.UUID, kind, reason, visible string) {
	s.logger.Warn(
		"continuation stopped",
		"decision", kind,
		"reason", reason,
		"room_id", roomID.String(),
		"chain_id", chainID.String(),
	)
	if visible != "" {
		s.noteSystem(ctx, roomID, visible)
	}
}

// cancelRoomContinuations cancels every pending or running follow-up in a room
// and leaves a visible reason. Called when Boss types, archives, or presses
// "hentikan".
func (s *Server) cancelRoomContinuations(ctx context.Context, roomID uuid.UUID, reason, visible string) int {
	now := s.now()
	rows, err := s.store.CancelPendingContinuationsForRoom(ctx, store.CancelPendingContinuationsForRoomParams{
		RoomID:     roomID,
		Reason:     reason,
		FinishedAt: &now,
	})
	if err != nil {
		s.logger.Error("cannot cancel continuations", "error", err, "room_id", roomID.String())
		return 0
	}
	if len(rows) == 0 {
		return 0
	}
	s.logger.Warn("continuation cancelled", "reason", reason, "room_id", roomID.String(), "count", len(rows))
	s.noteSystem(ctx, roomID, visible)
	return len(rows)
}

// StartContinuations recovers chains interrupted by a restart and then polls
// the database. A pending row inside its window is not dropped: it is claimed
// as soon as it is due.
func (s *Server) StartContinuations(ctx context.Context) {
	resumed, err := s.store.ResumeRunningContinuations(ctx)
	if err != nil {
		s.logger.Error("cannot resume continuations", "error", err)
	} else if len(resumed) > 0 {
		s.logger.Info("continuation chains resumed after restart", "count", len(resumed))
	}

	s.runDueContinuations(ctx)
	go func() {
		ticker := time.NewTicker(s.continuationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runDueContinuations(ctx)
			}
		}
	}()
}

// runDueContinuations claims a small batch and runs each chain independently:
// one 30 minute wait must not block another room.
func (s *Server) runDueContinuations(ctx context.Context) {
	now := s.now()
	rows, err := s.store.ClaimDueContinuations(ctx, store.ClaimDueContinuationsParams{
		StartedAt: &now,
		Limit:     continuationBatch,
	})
	if err != nil {
		s.logger.Error("cannot claim continuations", "error", err)
		return
	}
	if len(rows) > 0 {
		s.logger.Info("continuations claimed", "count", len(rows), "at", now.Format(time.RFC3339Nano))
	}
	for _, row := range rows {
		row := row
		go s.processContinuation(context.WithoutCancel(ctx), row)
	}
}

type continuationMeta = continuation.Meta

func parseMeta(raw []byte) continuation.Meta {
	var meta continuation.Meta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &meta)
	}
	return meta
}

func (s *Server) processContinuation(ctx context.Context, row store.Continuation) {
	finish := func(status, reason string) {
		now := s.now()
		if _, err := s.store.FinishContinuation(ctx, store.FinishContinuationParams{
			ID:         row.ID,
			Status:     status,
			Reason:     reason,
			FinishedAt: &now,
		}); err != nil {
			s.logger.Error("cannot finish continuation", "error", err, "continuation_id", row.ID.String())
		}
	}

	room, err := s.store.GetRoom(ctx, row.RoomID)
	if err != nil {
		s.logger.Error("continuation room is gone", "error", err, "room_id", row.RoomID.String())
		finish("skipped", "room tidak ditemukan")
		return
	}
	if room.ArchivedAt != nil {
		s.stopChain(ctx, room.ID, row.ChainID, "cancel", "room diarsipkan", "lanjut otomatis dibatalkan — room diarsipkan.")
		finish("cancelled", "room diarsipkan")
		return
	}

	root, rootErr := s.store.GetMessage(ctx, row.ChainID)
	rootCreatedAt := row.Deadline.Add(-continuation.Window)
	if rootErr == nil {
		rootCreatedAt = root.CreatedAt
	}

	newer, err := s.store.HasNewerUserMessage(ctx, store.HasNewerUserMessageParams{
		RoomID:    room.ID,
		CreatedAt: rootCreatedAt,
	})
	if err != nil {
		s.logger.Error("cannot check for newer user message", "error", err, "room_id", room.ID.String())
		finish("skipped", "tidak bisa memeriksa pesan baru")
		return
	}
	if newer {
		s.stopChain(ctx, room.ID, row.ChainID, "cancel", "ada pesan baru dari Boss", "lanjut otomatis dibatalkan — ada pesan baru dari Boss.")
		finish("cancelled", "ada pesan baru dari Boss")
		return
	}
	if !s.now().Before(row.Deadline) {
		s.stopChain(ctx, room.ID, row.ChainID, "capped", "batas 30 menit tercapai", "lanjut otomatis berhenti — batas 30 menit tercapai.")
		finish("capped", "batas 30 menit tercapai")
		return
	}

	procs := s.previousProcs(ctx, room.ID)
	s.noteSystem(ctx, room.ID, fmt.Sprintf(
		"lanjut otomatis (%d/%d) — menunggu proses yang kamu jalankan",
		row.Attempt, continuation.MaxPerChain,
	))

	// A continuation is a real turn too: a phone may attach mid-follow-up.
	s.hub.Reset(room.ID)
	streaming, startErr := s.startAssistant(ctx, room)
	if startErr != nil {
		s.logger.Error("cannot start continuation message", "error", startErr, "room_id", room.ID.String())
	}
	messageID := uuid.Nil
	if streaming != nil {
		messageID = streaming.ID
	}
	partial := s.newPartialWriter(ctx, messageID)

	outcome, streamErr := s.runTurn(
		ctx,
		room,
		continuationPrompt(int(row.Attempt), procs),
		func(name string, payload any) { s.publishRoomEvent(room.ID, name, payload) },
		partial.update,
		func(runID string) { s.setAssistantRunID(ctx, messageID, runID) },
	)
	var saved *store.Message
	if messageID != uuid.Nil {
		if outcome.rawBody == "" {
			s.discardAssistant(ctx, messageID)
		} else {
			state := "done"
			if streamErr != nil {
				state = "interrupted"
			}
			saved, err = s.finalizeAssistant(ctx, room, messageID, outcome.rawBody, outcome.runID, outcome.meta, state)
			if err != nil {
				s.logger.Error("cannot save continuation reply", "error", err, "room_id", room.ID.String())
			}
		}
	}
	var usageMessageID *uuid.UUID
	if saved != nil {
		usageMessageID = &saved.ID
	}
	s.recordUsage(ctx, room, outcome, usageMessageID)

	if saved == nil {
		reason := "lanjut tidak menghasilkan balasan"
		if streamErr != nil {
			reason = "Hermes tidak menjawab: " + streamErr.Error()
		}
		s.logger.Warn("continuation produced nothing", "reason", reason, "room_id", room.ID.String(), "continuation_id", row.ID.String())
		s.noteSystem(ctx, room.ID, "lanjut otomatis gagal — Hermes tidak menjawab. Coba tanya lagi.")
		if streamErr != nil {
			s.publishRoomEvent(room.ID, "error", map[string]string{"message": "Hermes did not finish this turn. Try again."})
		}
		s.hub.Reset(room.ID)
		s.publishRoomEvent(room.ID, "done", map[string]string{"messageId": ""})
		finish("skipped", reason)
		return
	}

	// Boss may have typed (or archived) while this continuation was waiting.
	// The row is already cancelled in the database; never overwrite that with
	// "done" and never schedule another follow-up behind a cancelled chain.
	if current, err := s.store.GetContinuation(ctx, row.ID); err == nil && current.Status == "cancelled" {
		s.logger.Info(
			"continuation cancelled while its turn was running",
			"room_id", room.ID.String(),
			"continuation_id", row.ID.String(),
			"reason", current.Reason,
		)
		return
	}

	// The pump has no browser attached by definition; notify unless another
	// tab happens to be watching this room.
	if s.hub.Count(room.ID) == 0 {
		s.notifyPush(ctx, room, saved.Body, saved.ID.String(), false)
	}

	s.scheduleAfterTurn(ctx, room, chainRef{
		id:        row.ChainID,
		createdAt: rootCreatedAt,
		deadline:  row.Deadline,
	}, true, outcome.meta)
	s.hub.Reset(room.ID)
	s.publishRoomEvent(room.ID, "done", map[string]string{"messageId": saved.ID.String()})
	finish("done", "lanjutan selesai")
}

// previousProcs reads the most recent assistant message's stored ids. The
// continuation is told exactly what to check; it is never asked to guess.
func (s *Server) previousProcs(ctx context.Context, roomID uuid.UUID) []string {
	row, err := s.store.GetLatestAssistantMessage(ctx, roomID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("cannot read previous assistant meta", "error", err, "room_id", roomID.String())
		}
		return nil
	}
	return parseMeta(row.Meta).Procs
}

func continuationPrompt(attempt int, procs []string) string {
	procsLine := "Tidak ada id proses yang tersimpan — periksa daftar proses dengan process_manage."
	if len(procs) > 0 {
		procsLine = "Proses yang tercatat: " + joinProcs(procs) + "."
	}
	return fmt.Sprintf(`Lanjut otomatis (%d/%d). Ada pekerjaan latar yang kamu mulai dan belum kamu laporkan hasilnya.
%s
Periksa dengan process_manage (poll atau wait). Kalau sudah selesai, laporkan hasil sebenarnya sekarang. Kalau masih berjalan, tunggu sampai selesai lalu laporkan hasilnya. Kalau prosesnya tidak ditemukan, katakan terus terang — jangan mengarang kemajuan.
Jawab singkat dalam bahasa Boss, lalu akhiri dengan STATUS: DONE — <hasil> atau STATUS: BLOCKED — <yang kamu butuhkan>.`,
		attempt, continuation.MaxPerChain, procsLine)
}

func joinProcs(procs []string) string {
	out := ""
	for i, id := range procs {
		if i > 0 {
			out += ", "
		}
		out += id
	}
	return out
}

type continuationView struct {
	ID       string    `json:"id"`
	Attempt  int32     `json:"attempt"`
	Status   string    `json:"status"`
	DueAt    time.Time `json:"due_at"`
	Deadline time.Time `json:"deadline"`
}

func (s *Server) getContinuation(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	if _, err := s.store.GetRoom(c.Request().Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(404, errorBody("There is no room with that id."))
		}
		s.logger.Error("cannot load room for continuation", "error", err)
		return c.JSON(500, errorBody("Iris tidak bisa membuka room ini."))
	}
	rows, err := s.store.ListPendingContinuations(c.Request().Context(), id)
	if err != nil {
		s.logger.Error("cannot list continuations", "error", err)
		return c.JSON(500, errorBody("Iris tidak bisa memuat status lanjutan."))
	}
	if len(rows) == 0 {
		return c.JSON(200, map[string]any{"continuation": nil})
	}
	row := rows[0]
	return c.JSON(200, map[string]any{"continuation": continuationView{
		ID:       row.ID.String(),
		Attempt:  row.Attempt,
		Status:   row.Status,
		DueAt:    row.DueAt,
		Deadline: row.Deadline,
	}})
}

func (s *Server) cancelContinuation(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	if _, err := s.store.GetRoom(c.Request().Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(404, errorBody("There is no room with that id."))
		}
		s.logger.Error("cannot load room to cancel", "error", err)
		return c.JSON(500, errorBody("Iris tidak bisa membuka room ini."))
	}
	count := s.cancelRoomContinuations(
		c.Request().Context(),
		id,
		"dihentikan oleh Boss",
		"lanjut otomatis dibatalkan — kamu menghentikannya.",
	)
	return c.JSON(200, map[string]any{"cancelled": count})
}
