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
	"sync"
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

	// Boss' own sentence can carry a reminder. Parse before dispatch; if there
	// is no explicit time this is a no-op and Hermes answers normally.
	createdReminder, _ := s.createParsedReminder(c.Request().Context(), room, text)

	attachments, err := s.loadAttachments(c.Request().Context(), room.ID, attachmentIDs)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Lampiran itu tidak ada di room ini."))
	}

	userMessage, err := s.store.InsertMessage(c.Request().Context(), store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Role:   "user",
		Body:   text,
		Meta:   []byte("{}"),
	})
	if err != nil {
		s.logger.Error("cannot save user message", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan pesanmu. Tidak ada yang terkirim."))
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

	promptText := text
	if len(attachments) > 0 {
		lines := make([]string, 0, len(attachments))
		for _, f := range attachments {
			lines = append(lines, fmt.Sprintf("Lampiran: %s (%s, %s)", f.Path, files.HumanType(f.Mime, f.Name), files.FormatSize(f.Size)))
		}
		if promptText == "" {
			promptText = strings.Join(lines, "\n") + "\n\nBoss mengirim lampiran tanpa pesan."
		} else {
			promptText = strings.Join(lines, "\n") + "\n\n" + promptText
		}
	}

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

	var gone atomic.Bool
	finished := make(chan struct{})
	defer close(finished)
	// Capture the request context now: the goroutine outlives the handler under
	// Echo, and reading c.Request() after ServeHTTP reuses the context is a data
	// race (found by `go test -race`).
	reqCtx := c.Request().Context()
	go func() {
		select {
		case <-reqCtx.Done():
			gone.Store(true)
			s.logger.Info("client left; the turn continues", "room_id", room.ID.String())
		case <-finished:
		}
	}()

	var sendMu sync.Mutex
	send := func(event string, payload any) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if gone.Load() {
			return
		}
		body, err := json.Marshal(payload)
		if err != nil {
			s.logger.Error("cannot encode sse payload", "error", err)
			return
		}
		if _, err := fmt.Fprintf(c.Response(), "event: %s\ndata: %s\n\n", event, body); err != nil {
			gone.Store(true)
			s.logger.Info("client left while writing; the turn continues", "room_id", room.ID.String())
			return
		}
		c.Response().Flush()
	}

	// Approvals are room events, not turn-only frames: another request (the
	// answer, a sweeper, or a fixture) may publish while this stream is open.
	events, unsubscribe := s.hub.subscribe(room.ID)
	hubDone := make(chan struct{})
	defer func() {
		close(hubDone)
		unsubscribe()
	}()
	go func() {
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				send(ev.Name, ev.Payload)
				if ev.ack != nil {
					close(ev.ack)
				}
			case <-hubDone:
				return
			}
		}
	}()

	if createdReminder != nil {
		send("reminder", viewReminder(*createdReminder))
	}

	outcome, streamErr := s.runTurn(turnCtx, room, promptText, send)
	rawBody := outcome.rawBody

	var saved *store.Message
	if rawBody != "" {
		row, err := s.persistAssistant(turnCtx, room, rawBody, outcome.runID, outcome.meta)
		if err != nil {
			s.logger.Error("cannot save assistant message", "error", err, "room_id", room.ID.String())
		} else {
			saved = row
		}
	}

	// Nobody watching? A finished turn gets one push. A phone that is still
	// attached to this room already sees the reply and stays quiet.
	if streamErr == nil && saved != nil && !outcome.stopped {
		watchers := s.hub.count(room.ID)
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
		send("error", map[string]string{"message": message})
		if saved != nil {
			send("done", map[string]string{"messageId": saved.ID.String()})
		}
		return nil
	}

	if saved != nil {
		send("done", map[string]string{"messageId": saved.ID.String()})
	} else {
		send("done", map[string]string{"messageId": ""})
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
