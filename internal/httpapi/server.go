// Package httpapi is Iris' JSON + SSE surface. It owns room and message
// persistence and proxies one Hermes session per room.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/auth"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/store"
)

// Store is the persistence surface handlers need. sqlc's *store.Queries
// satisfies it; tests use an in-memory implementation.
type Store interface {
	CreateRoom(ctx context.Context, arg store.CreateRoomParams) (store.Room, error)
	ListRooms(ctx context.Context) ([]store.Room, error)
	GetRoom(ctx context.Context, id uuid.UUID) (store.Room, error)
	ArchiveRoom(ctx context.Context, id uuid.UUID) (store.Room, error)

	InsertMessage(ctx context.Context, arg store.InsertMessageParams) (store.Message, error)
	ListMessages(ctx context.Context, arg store.ListMessagesParams) ([]store.Message, error)
}

// Server wires the HTTP surface.
type Server struct {
	store   Store
	hermes  hermes.Client
	auth    *auth.Service
	logger  *slog.Logger
	version string
}

// Options configures a Server.
type Options struct {
	Store   Store
	Hermes  hermes.Client
	Auth    *auth.Service
	Logger  *slog.Logger
	Version string
}

// New builds the Echo application with all routes registered.
func New(opts Options) *echo.Echo {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		store:   opts.Store,
		hermes:  opts.Hermes,
		auth:    opts.Auth,
		logger:  logger,
		version: opts.Version,
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = s.httpErrorHandler

	e.Use(s.auth.Middleware)

	e.GET("/api/health", s.health)
	e.POST("/api/auth/login", s.auth.Login)
	e.GET("/api/auth/me", s.auth.Me)
	e.POST("/api/auth/logout", s.auth.Logout)

	e.POST("/api/rooms", s.createRoom)
	e.GET("/api/rooms", s.listRooms)
	e.GET("/api/rooms/:id", s.getRoom)
	e.POST("/api/rooms/:id/archive", s.archiveRoom)
	e.GET("/api/rooms/:id/messages", s.listMessages)
	e.POST("/api/rooms/:id/messages", s.streamMessage)

	return e
}

func (s *Server) health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"ok":      true,
		"version": s.version,
	})
}

func (s *Server) httpErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	code := http.StatusInternalServerError
	message := "Something went wrong. Try again."

	var he *echo.HTTPError
	if errors.As(err, &he) {
		code = he.Code
		switch code {
		case http.StatusNotFound:
			message = "That is not here."
		case http.StatusMethodNotAllowed:
			message = "That method is not allowed."
		case http.StatusUnauthorized:
			message = "Sign in to Iris first."
		case http.StatusBadRequest:
			message = "That request could not be read."
		}
	}
	if code >= 500 {
		s.logger.Error("http error", "code", code, "error", err)
	}
	if err := c.JSON(code, errorBody(message)); err != nil {
		s.logger.Error("cannot write error response", "error", err)
	}
}

func errorBody(message string) map[string]string {
	return map[string]string{"error": message}
}

// roomView is the browser-facing room shape; the Hermes session id stays
// server-side.
type roomView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Topic      string     `json:"topic"`
	CreatedAt  time.Time  `json:"created_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

func viewRoom(r store.Room) roomView {
	return roomView{
		ID:         r.ID.String(),
		Name:       r.Name,
		Topic:      r.Topic,
		CreatedAt:  r.CreatedAt,
		ArchivedAt: r.ArchivedAt,
	}
}

type messageView struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	Role      string    `json:"role"`
	Body      string    `json:"body"`
	RunID     *string   `json:"run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func viewMessage(m store.Message) messageView {
	return messageView{
		ID:        m.ID.String(),
		RoomID:    m.RoomID.String(),
		Role:      m.Role,
		Body:      m.Body,
		RunID:     m.RunID,
		CreatedAt: m.CreatedAt,
	}
}
