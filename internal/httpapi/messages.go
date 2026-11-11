package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

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

	messages := make([]messageView, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, viewMessage(row))
	}
	return c.JSON(http.StatusOK, map[string]any{"messages": messages})
}

type sendMessageRequest struct {
	Text string `json:"text"`
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
	if text == "" {
		return c.JSON(http.StatusBadRequest, errorBody("Write a message first."))
	}

	room, err := s.store.GetRoom(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("There is no room with that id."))
		}
		s.logger.Error("cannot load room for message", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not open that room."))
	}

	if _, err := s.store.InsertMessage(c.Request().Context(), store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Role:   "user",
		Body:   text,
	}); err != nil {
		s.logger.Error("cannot save user message", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not save your message. Nothing was sent."))
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
	go func() {
		select {
		case <-c.Request().Context().Done():
			gone.Store(true)
			s.logger.Info("client left; the turn continues", "room_id", room.ID.String())
		case <-finished:
		}
	}()

	send := func(event string, payload any) {
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

	var (
		assistant strings.Builder
		finalText string
		runID     string
	)

	streamErr := s.hermes.StreamChat(turnCtx, room.HermesSessionID, text, func(ev hermes.Event) error {
		if ev.RunID != "" {
			runID = ev.RunID
		}
		switch ev.Kind {
		case "delta":
			assistant.WriteString(ev.Text)
			send("delta", map[string]string{"text": ev.Text})
		case "tool":
			name := ev.Name
			if name == "" {
				name = "tool"
			}
			status := ev.Status
			if status == "" {
				status = "running"
			}
			send("tool", map[string]string{"name": name, "status": status})
		case "done":
			if ev.Text != "" {
				finalText = ev.Text
			}
		case "error":
			message := ev.Text
			if message == "" {
				message = "Hermes reported a problem with this turn."
			}
			send("error", map[string]string{"message": message})
		case "unknown":
			raw := any(string(ev.Raw))
			if json.Valid(ev.Raw) {
				raw = json.RawMessage(ev.Raw)
			}
			send("unknown", map[string]any{"kind": ev.OriginalKind, "raw": raw})
		}
		return nil
	})

	body := assistant.String()
	if body == "" && finalText != "" {
		body = finalText
	}

	var saved *store.Message
	if body != "" {
		persistCtx, cancel := context.WithTimeout(context.Background(), persistTimeout)
		row, err := s.store.InsertMessage(persistCtx, store.InsertMessageParams{
			ID:     uuid.New(),
			RoomID: room.ID,
			Role:   "assistant",
			Body:   body,
			RunID:  optionalString(runID),
		})
		cancel()
		if err != nil {
			s.logger.Error("cannot save assistant message", "error", err, "room_id", room.ID.String())
		} else {
			saved = &row
		}
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

func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
