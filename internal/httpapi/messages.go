package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/artifacts"
	"github.com/aryadhira/iris/internal/continuation"
	"github.com/aryadhira/iris/internal/files"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/store"
)

const (
	defaultMessageLimit = 50
	maxMessageLimit     = 200
	persistTimeout      = 10 * time.Second
)

func (s *Server) listMessages(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	if _, err := s.store.GetRoom(c.Request().Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("There is no room with that id."))
		}
		s.logger.Error("cannot load room for messages", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not open that room."))
	}

	limit := int32(defaultMessageLimit)
	if raw := c.QueryParam("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return c.JSON(http.StatusBadRequest, errorBody("Limit must be a whole number between 1 and 200."))
		}
		if n > maxMessageLimit {
			n = maxMessageLimit
		}
		limit = int32(n)
	}

	var before *time.Time
	if raw := c.QueryParam("before"); raw != "" {
		ts, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errorBody("That message cursor is not a valid timestamp."))
		}
		before = &ts
	}

	rows, err := s.store.ListMessages(c.Request().Context(), store.ListMessagesParams{
		RoomID:   id,
		Before:   before,
		RowLimit: limit,
	})
	if err != nil {
		s.logger.Error("cannot list messages", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not load this room's messages."))
	}

	fileRows, err := s.store.ListFiles(c.Request().Context(), id)
	if err != nil {
		s.logger.Error("cannot list files for messages", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memuat lampiran room ini."))
	}
	byMessage := attachmentsByMessage(fileRows)

	messages := make([]messageView, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, viewMessage(row, byMessage[row.ID]))
	}
	return c.JSON(http.StatusOK, map[string]any{"messages": messages})
}

type sendMessageRequest struct {
	Text          string   `json:"text"`
	AttachmentIDs []string `json:"attachmentIds"`
}

// streamMessage persists the user message, streams the Hermes turn to the
// client as SSE, and persists the assistant text even if the client leaves.
func (s *Server) streamMessage(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}

	var req sendMessageRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Write a message first."))
	}
	text := strings.TrimSpace(req.Text)
	attachmentIDs, err := parseAttachmentIDs(req.AttachmentIDs)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Lampiran itu tidak valid."))
	}
	if text == "" && len(attachmentIDs) == 0 {
		return c.JSON(http.StatusBadRequest, errorBody("Tulis pesan atau lampirkan file dulu."))
	}
	if len(attachmentIDs) > maxAttachmentsPerMessage {
		return c.JSON(http.StatusBadRequest, errorBody("Maksimal 10 lampiran per pesan."))
	}

	room, err := s.store.GetRoom(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("There is no room with that id."))
		}
		s.logger.Error("cannot load room for message", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not open that room."))
	}

	// The budget is checked once, before anything is written: a refused message
	// leaves no user row, no reminder and no run behind.
	userMessageID := uuid.New()
	decision, guardErr := s.budgetCheck(
		c.Request().Context(),
		room,
		text,
		len(attachmentIDs) > 0,
	)
	if guardErr != nil {
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memeriksa batas biaya. Tidak ada yang terkirim."))
	}
	if !decision.Allowed {
		return c.JSON(http.StatusTooManyRequests, errorBody(decision.Refusal))
	}

	// Boss' own sentence can carry a reminder. Parse before dispatch; if there
	// is no explicit time this is a no-op and Hermes answers normally.
	createdReminder, _ := s.createParsedReminder(c.Request().Context(), room, text)

	attachments, err := s.loadAttachments(c.Request().Context(), room.ID, attachmentIDs)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Lampiran itu tidak ada di room ini."))
	}

	userMessage, err := s.store.InsertMessage(c.Request().Context(), store.InsertMessageParams{
		ID:     userMessageID,
		RoomID: room.ID,
		Role:   "user",
		Body:   text,
		Meta:   []byte("{}"),
	})
	if err != nil {
		s.logger.Error("cannot save user message", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan pesanmu. Tidak ada yang terkirim."))
	}
	// The user row exists now, so the override receipt can point at it. A
	// failed receipt is logged loudly but must not drop the message Boss sent.
	if decision.Override {
		if err := s.recordBudgetOverride(c.Request().Context(), room.ID, &userMessage.ID, decision.Spent, decision.Budget); err != nil {
			s.logger.Error("cannot log budget override", "error", err, "room_id", room.ID.String())
		}
	}
	for _, f := range attachments {
		if _, err := s.store.AttachFileToMessage(c.Request().Context(), store.AttachFileToMessageParams{
			MessageID: &userMessage.ID,
			ID:        f.ID,
			RoomID:    room.ID,
		}); err != nil {
			s.logger.Error("cannot attach file to message", "error", err, "file_id", f.ID.String())
		}
	}

	// Boss typed: the safety net must not talk over a live conversation. Any
	// pending chain is cancelled now, visibly, before the new turn streams.
	s.cancelRoomContinuations(
		context.WithoutCancel(c.Request().Context()),
		room.ID,
		"ada pesan baru dari Boss",
		"lanjut otomatis dibatalkan — ada pesan baru dari Boss.",
	)

	promptText := promptWithAttachments(text, attachments)

	return s.streamTurn(c, room, promptText, createdReminder)
}

// promptWithAttachments is shared by a new message and a retry, so a retried
// turn reaches Hermes with the same file lines the first attempt had.
func promptWithAttachments(text string, attachments []store.File) string {
	promptText := text
	if len(attachments) == 0 {
		return promptText
	}
	lines := make([]string, 0, len(attachments))
	for _, f := range attachments {
		lines = append(lines, fmt.Sprintf("Lampiran: %s (%s, %s)", f.Path, files.HumanType(f.Mime, f.Name), files.FormatSize(f.Size)))
	}
	if promptText == "" {
		return strings.Join(lines, "\n") + "\n\nBoss mengirim lampiran tanpa pesan."
	}
	return strings.Join(lines, "\n") + "\n\n" + promptText
}

func (s *Server) streamTurn(
	c echo.Context,
	room store.Room,
	promptText string,
	createdReminder *store.Reminder,
) error {
	headers := c.Response().Header()
	headers.Set(echo.HeaderContentType, "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	c.Response().WriteHeader(http.StatusOK)
	c.Response().Flush()

	// The turn is deliberately detached from the HTTP request: a phone that
	// walks into a lift must not kill the work (AGENT.md, no lost work).
	turnCtx := context.WithoutCancel(c.Request().Context())
	reqCtx := c.Request().Context()

	var gone atomic.Bool
	// Capture the request context now: the goroutine outlives the handler under
	// Echo, and reading c.Request() after ServeHTTP reuses the context is a data
	// race (found by `go test -race`).
	go func() {
		<-reqCtx.Done()
		gone.Store(true)
		s.logger.Info("client left; the turn continues", "room_id", room.ID.String())
	}()

	// This response is now just another room subscriber. The same frames go to
	// every phone/laptop attached to /events, so no browser owns the rendering.
	s.hub.BeginTurn(room.ID)
	_, events, unsubscribe := s.hub.Subscribe(room.ID)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for ev := range events {
			if gone.Load() {
				continue
			}
			body, err := json.Marshal(ev.Payload)
			if err != nil {
				s.logger.Error("cannot encode sse payload", "error", err)
				continue
			}
			if _, err := fmt.Fprintf(c.Response(), "event: %s\ndata: %s\n\n", ev.Name, body); err != nil {
				gone.Store(true)
				s.logger.Info("client left while writing; the turn continues", "room_id", room.ID.String())
				continue
			}
			c.Response().Flush()
		}
	}()
	defer func() {
		unsubscribe()
		<-pumpDone
	}()

	if createdReminder != nil {
		s.publishRoomEvent(room.ID, "reminder", viewReminder(*createdReminder))
	}

	// The assistant row exists before the first token. A reload during the turn
	// reads this row; a crash leaves it streaming for recovery to mark.
	streaming, err := s.startAssistant(turnCtx, room)
	if err != nil {
		s.logger.Error("cannot start streaming message", "error", err, "room_id", room.ID.String())
	}
	messageID := uuid.Nil
	if streaming != nil {
		messageID = streaming.ID
	}
	partial := s.newPartialWriter(turnCtx, messageID)

	outcome, streamErr := s.runTurn(
		turnCtx,
		room,
		promptText,
		func(name string, payload any) { s.publishRoomEvent(room.ID, name, payload) },
		partial.update,
		func(runID string) {
			s.setAssistantRunID(turnCtx, messageID, runID)
			// A client that attached before this turn began must learn it exists; otherwise the room
			// reads "idle" while the agent is working (seen with a second tab and with the phone).
			s.publishRoomEvent(room.ID, "run", map[string]any{
				"messageId": messageID.String(),
				"run": map[string]any{
					"id":           runID,
					"status":       "running",
					"started_at":   time.Now().UTC().Format(time.RFC3339),
					"continuation": 0,
				},
			})
		},
	)
	rawBody := outcome.rawBody

	var saved *store.Message
	state := "done"
	if streamErr != nil {
		state = "interrupted"
	}
	if messageID != uuid.Nil {
		if rawBody == "" {
			s.discardAssistant(turnCtx, messageID)
		} else {
			row, err := s.finalizeAssistant(turnCtx, room, messageID, rawBody, outcome.runID, outcome.meta, state)
			if err != nil {
				s.logger.Error("cannot save assistant message", "error", err, "room_id", room.ID.String())
			} else {
				saved = row
			}
		}
	}

	// Meter the turn whether or not it produced visible text: a stopped run
	// still spent tokens, and a terminal payload without usage must leave a
	// visible "not reported" row.
	var messageIDPtr *uuid.UUID
	if saved != nil {
		messageIDPtr = &saved.ID
	}
	s.recordUsage(turnCtx, room, outcome, messageIDPtr)

	// Nobody watching? A finished turn gets one push. A phone that is still
	// attached to this room already sees the reply and stays quiet.
	if streamErr == nil && saved != nil && !outcome.stopped {
		watchers := s.hub.Count(room.ID)
		if gone.Load() && watchers > 0 {
			watchers--
		}
		if watchers == 0 {
			s.notifyPush(turnCtx, room, saved.Body, saved.ID.String(), false)
		}
	}

	// The safety net only arms after a clean turn. A turn that died mid-stream
	// has no trustworthy "what is still running" claim.
	// A stop or a refused/expired approval is a decision. The safety net must
	// not resurrect it with an automatic follow-up.
	if streamErr == nil && saved != nil && !outcome.stopped &&
		!s.runWasDenied(turnCtx, room.ID, outcome.runID) && !s.runIsStopped(turnCtx, outcome.runID) {
		s.scheduleAfterTurn(turnCtx, room, chainRef{
			id:        saved.ID,
			createdAt: saved.CreatedAt,
			deadline:  saved.CreatedAt.Add(continuation.Window),
		}, false, outcome.meta)
	}

	if streamErr != nil {
		s.logger.Error("hermes turn failed", "error", streamErr, "room_id", room.ID.String())
		message := "Hermes did not finish this turn. Try again."
		if errors.Is(streamErr, hermes.ErrIdle) {
			message = "Hermes went quiet, so the turn stopped. Anything it wrote is kept below."
		}
		s.publishRoomEvent(room.ID, "error", map[string]string{"message": message})
		s.hub.Reset(room.ID)
		if saved != nil {
			s.publishRoomEvent(room.ID, "done", map[string]string{"messageId": saved.ID.String()})
		} else {
			s.publishRoomEvent(room.ID, "done", map[string]string{"messageId": ""})
		}
		return nil
	}

	s.hub.Reset(room.ID)
	if saved != nil {
		s.publishRoomEvent(room.ID, "done", map[string]string{"messageId": saved.ID.String()})
	} else {
		s.publishRoomEvent(room.ID, "done", map[string]string{"messageId": ""})
	}
	return nil
}

const maxAttachmentsPerMessage = 10

func parseAttachmentIDs(raw []string) ([]uuid.UUID, error) {
	seen := make(map[uuid.UUID]struct{}, len(raw))
	out := make([]uuid.UUID, 0, len(raw))
	for _, value := range raw {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		id, err := uuid.Parse(trimmed)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func (s *Server) loadAttachments(ctx context.Context, roomID uuid.UUID, ids []uuid.UUID) ([]store.File, error) {
	out := make([]store.File, 0, len(ids))
	for _, id := range ids {
		row, err := s.store.GetFile(ctx, id)
		if err != nil {
			return nil, err
		}
		if row.RoomID != roomID || row.Source != "user" || row.MessageID != nil {
			return nil, errors.New("attachment is not available in this room")
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Server) saveArtifact(room store.Room, messageID uuid.UUID, item artifacts.Found) {
	info, err := os.Stat(item.Abs)
	if err != nil {
		s.logger.Warn("artifact vanished before it could be stored", "path", item.Path, "error", err)
		return
	}
	mimeType, err := files.DetectFile(item.Abs, path.Base(item.Path))
	if err != nil {
		s.logger.Warn("cannot identify artifact", "path", item.Path, "error", err)
		return
	}
	sum, err := hashFile(item.Abs)
	if err != nil {
		s.logger.Warn("cannot hash artifact", "path", item.Path, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	_, err = s.store.InsertFile(ctx, store.InsertFileParams{
		ID:        uuid.New(),
		RoomID:    room.ID,
		MessageID: &messageID,
		Source:    "agent",
		Name:      path.Base(item.Path),
		Path:      item.Path,
		Mime:      mimeType,
		Size:      info.Size(),
		Sha256:    sum,
	})
	if err != nil {
		s.logger.Error("cannot save artifact row", "error", err, "path", item.Path)
	}
}

func hashFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
