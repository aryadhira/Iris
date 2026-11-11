package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/store"
)

type createRoomRequest struct {
	Name  string `json:"name"`
	Topic string `json:"topic"`
}

func (s *Server) createRoom(c echo.Context) error {
	var req createRoomRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Give the room a name and an optional topic."))
	}
	name := strings.TrimSpace(req.Name)
	topic := strings.TrimSpace(req.Topic)
	if name == "" {
		return c.JSON(http.StatusBadRequest, errorBody("A room needs a name."))
	}
	if len(name) > 200 || len(topic) > 4000 {
		return c.JSON(http.StatusBadRequest, errorBody("That room name or topic is too long."))
	}

	// Hermes rejects a duplicate session title ("Title 'X' is already in use"), and a user
	// will absolutely create two rooms called "Work". The room keeps the name they typed;
	// the Hermes session gets a stable, unique title derived from the room id.
	roomID := uuid.New()
	title := name + " · " + roomID.String()[:8]

	// Hermes first: a room without a session is useless, and a failed session
	// must not leave a row behind. The call is detached from the request so a
	// phone that drops mid-create cannot strand an orphan session.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 15*time.Second)
	sessionID, err := s.hermes.CreateSession(ctx, title)
	cancel()
	if err != nil {
		s.logger.Error("cannot create hermes session", "error", err)
		return c.JSON(http.StatusBadGateway, errorBody("Hermes is not answering. No room was created."))
	}

	row, err := s.store.CreateRoom(c.Request().Context(), store.CreateRoomParams{
		ID:              roomID,
		Name:            name,
		Topic:           topic,
		HermesSessionID: sessionID,
	})
	if err != nil {
		s.logger.Error("cannot save room; cleaning up hermes session", "error", err, "hermes_session_id", sessionID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if derr := s.hermes.DeleteSession(cleanupCtx, sessionID); derr != nil {
			s.logger.Error("cannot clean up orphan hermes session", "error", derr, "hermes_session_id", sessionID)
		}
		cleanupCancel()
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not save the room. Nothing was created."))
	}

	return c.JSON(http.StatusCreated, map[string]any{"room": viewRoom(row)})
}

func (s *Server) listRooms(c echo.Context) error {
	rows, err := s.store.ListRooms(c.Request().Context())
	if err != nil {
		s.logger.Error("cannot list rooms", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not load your rooms."))
	}
	rooms := make([]roomView, 0, len(rows))
	for _, row := range rows {
		rooms = append(rooms, viewRoom(row))
	}
	return c.JSON(http.StatusOK, map[string]any{"rooms": rooms})
}

func (s *Server) getRoom(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	row, err := s.store.GetRoom(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("There is no room with that id."))
		}
		s.logger.Error("cannot get room", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not open that room."))
	}
	return c.JSON(http.StatusOK, map[string]any{"room": viewRoom(row)})
}

func (s *Server) archiveRoom(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	row, err := s.store.ArchiveRoom(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, getErr := s.store.GetRoom(c.Request().Context(), id)
			if getErr != nil {
				return c.JSON(http.StatusNotFound, errorBody("There is no room with that id."))
			}
			return c.JSON(http.StatusOK, map[string]any{"room": viewRoom(existing)})
		}
		s.logger.Error("cannot archive room", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not archive that room."))
	}
	return c.JSON(http.StatusOK, map[string]any{"room": viewRoom(row)})
}

func roomID(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, c.JSON(http.StatusNotFound, errorBody("There is no room with that id."))
	}
	return id, nil
}
