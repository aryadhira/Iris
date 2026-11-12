package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/store"
)

// retryMessage re-runs the turn behind an existing USER message. It never
// inserts a second user row: a resend would be a different conversation, not a
// retry. The reply that is being replaced stays in the transcript, dimmed and
// labelled `superseded` — deleting it would lie about what happened.
func (s *Server) retryMessage(c echo.Context) error {
	roomUUID, err := roomID(c)
	if err != nil {
		return err
	}
	mid, err := uuid.Parse(strings.TrimSpace(c.Param("mid")))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Pesan itu tidak valid."))
	}

	ctx := c.Request().Context()
	room, err := s.store.GetRoom(ctx, roomUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("There is no room with that id."))
		}
		s.logger.Error("cannot load room for retry", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not open that room."))
	}
	userMessage, err := s.store.GetMessage(ctx, mid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Pesan itu tidak ada."))
		}
		s.logger.Error("cannot load message for retry", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membaca pesan itu."))
	}
	if userMessage.RoomID != room.ID || userMessage.Role != "user" {
		return c.JSON(http.StatusBadRequest, errorBody("Hanya pesanmu di room ini yang bisa dicoba lagi."))
	}

	if s.roomBusy(ctx, room.ID) {
		return c.JSON(http.StatusConflict, errorBody("Room ini masih bekerja. Tunggu balasannya selesai dulu, lalu coba lagi."))
	}

	// A retry spends money too. There is no `lanjut` shortcut here: the override
	// is a normal user message, and it must be deliberate.
	decision, guardErr := s.budgetCheck(ctx, room, "", false)
	if guardErr != nil {
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memeriksa batas biaya. Tidak ada yang dikirim."))
	}
	if !decision.Allowed {
		return c.JSON(http.StatusTooManyRequests, errorBody(decision.Refusal))
	}

	if err := s.supersedeReplyAfter(ctx, room.ID, userMessage); err != nil {
		s.logger.Error("cannot mark the previous reply superseded", "error", err, "room_id", room.ID.String())
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menandai balasan lama. Coba lagi."))
	}

	s.cancelRoomContinuations(
		ctx,
		room.ID,
		"pesan diulang oleh Boss",
		"lanjut otomatis dibatalkan — pesan diulang oleh Boss.",
	)

	attachments, err := s.attachmentsForMessage(ctx, room.ID, userMessage.ID)
	if err != nil {
		s.logger.Error("cannot load attachments for retry", "error", err, "room_id", room.ID.String())
	}
	prompt := promptWithAttachments(userMessage.Body, attachments)
	return s.streamTurn(c, room, prompt, nil)
}

// roomBusy is the honest answer to "can a retry start now". A run row covers a
// turn that started; an attached turn stream covers the first moments before
// Hermes announced its run id.
func (s *Server) roomBusy(ctx context.Context, roomID uuid.UUID) bool {
	if s.hub.Count(roomID) > 0 {
		return true
	}
	rows, err := s.store.ListActiveRuns(ctx, roomID)
	if err != nil {
		s.logger.Error("cannot list active runs for retry", "error", err, "room_id", roomID.String())
		return true // refuse rather than start two turns on an unreadable state
	}
	return len(rows) > 0
}

// supersedeReplyAfter walks from the retried user message to the next user
// message and marks every assistant reply in that window. System lines (a
// continuation notice, a reminder) are left alone.
func (s *Server) supersedeReplyAfter(ctx context.Context, roomID uuid.UUID, userMessage store.Message) error {
	rows, err := s.store.ListMessagesAfter(ctx, store.ListMessagesAfterParams{
		RoomID:    roomID,
		CreatedAt: userMessage.CreatedAt,
		Column3:   userMessage.ID,
	})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Role == "user" {
			break
		}
		if row.Role != "assistant" {
			continue
		}
		if _, err := s.store.MarkMessageSuperseded(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) attachmentsForMessage(ctx context.Context, roomID, messageID uuid.UUID) ([]store.File, error) {
	files, err := s.store.ListFiles(ctx, roomID)
	if err != nil {
		return nil, err
	}
	out := make([]store.File, 0)
	for _, file := range files {
		if file.MessageID != nil && *file.MessageID == messageID {
			out = append(out, file)
		}
	}
	return out, nil
}
