// Package httpapi is Iris' JSON + SSE surface. It owns room and message
// persistence and proxies one Hermes session per room.
package httpapi

import (
	"context"
	"encoding/json"
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
	UpdateRoomPrompt(ctx context.Context, arg store.UpdateRoomPromptParams) (store.Room, error)

	InsertMessage(ctx context.Context, arg store.InsertMessageParams) (store.Message, error)
	ListMessages(ctx context.Context, arg store.ListMessagesParams) ([]store.Message, error)
	GetMessage(ctx context.Context, id uuid.UUID) (store.Message, error)
	GetLatestAssistantMessage(ctx context.Context, roomID uuid.UUID) (store.Message, error)

	CreateContinuation(ctx context.Context, arg store.CreateContinuationParams) (store.Continuation, error)
	GetContinuation(ctx context.Context, id uuid.UUID) (store.Continuation, error)
	ListPendingContinuations(ctx context.Context, roomID uuid.UUID) ([]store.Continuation, error)
	ListContinuationsForChain(ctx context.Context, chainID uuid.UUID) ([]store.Continuation, error)
	CountContinuationsForChain(ctx context.Context, chainID uuid.UUID) (int64, error)
	ClaimDueContinuations(ctx context.Context, arg store.ClaimDueContinuationsParams) ([]store.Continuation, error)
	FinishContinuation(ctx context.Context, arg store.FinishContinuationParams) (store.Continuation, error)
	CancelPendingContinuationsForRoom(ctx context.Context, arg store.CancelPendingContinuationsForRoomParams) ([]store.Continuation, error)
	ResumeRunningContinuations(ctx context.Context) ([]store.Continuation, error)
	HasNewerUserMessage(ctx context.Context, arg store.HasNewerUserMessageParams) (bool, error)

	InsertFile(ctx context.Context, arg store.InsertFileParams) (store.File, error)
	GetFile(ctx context.Context, id uuid.UUID) (store.File, error)
	ListFiles(ctx context.Context, roomID uuid.UUID) ([]store.File, error)
	DeleteFile(ctx context.Context, id uuid.UUID) (store.File, error)
	AttachFileToMessage(ctx context.Context, arg store.AttachFileToMessageParams) (int64, error)
}

// Server wires the HTTP surface.
type Server struct {
	store   Store
	hermes  hermes.Client
	auth    *auth.Service
	logger  *slog.Logger
	version string
	dataDir string

	// now and continuationInterval are seams for the pump's tests: a fake
	// clock must be able to cross the 30 minute cap without sleeping.
	now                  func() time.Time
	continuationInterval time.Duration
}

// Options configures a Server.
type Options struct {
	Store                Store
	Hermes               hermes.Client
	Auth                 *auth.Service
	Logger               *slog.Logger
	Version              string
	DataDir              string
	Now                  func() time.Time
	ContinuationInterval time.Duration
	RunContinuations     bool
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
		dataDir: opts.DataDir,
		now:     opts.Now,
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.continuationInterval = opts.ContinuationInterval
	if s.continuationInterval == 0 {
		s.continuationInterval = time.Second
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
	e.PATCH("/api/rooms/:id/prompt", s.updateRoomPrompt)
	e.GET("/api/rooms/:id/messages", s.listMessages)
	e.POST("/api/rooms/:id/messages", s.streamMessage)
	e.GET("/api/rooms/:id/continuation", s.getContinuation)
	e.POST("/api/rooms/:id/continuation/cancel", s.cancelContinuation)
	e.GET("/api/rooms/:id/files", s.listRoomFiles)
	e.POST("/api/rooms/:id/uploads", s.uploadFile)

	e.GET("/api/files/:id", s.downloadFile)
	e.DELETE("/api/files/:id", s.deleteFile)

	if opts.RunContinuations {
		s.StartContinuations(context.Background())
	}

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

type attachmentView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
}

func viewAttachment(f store.File) attachmentView {
	return attachmentView{
		ID:     f.ID.String(),
		Name:   f.Name,
		Mime:   f.Mime,
		Size:   f.Size,
		Sha256: f.Sha256,
	}
}

type fileView struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	MessageID *string   `json:"message_id,omitempty"`
	Source    string    `json:"source"`
	Name      string    `json:"name"`
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	Sha256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

func viewFile(f store.File) fileView {
	var messageID *string
	if f.MessageID != nil {
		value := f.MessageID.String()
		messageID = &value
	}
	return fileView{
		ID:        f.ID.String(),
		RoomID:    f.RoomID.String(),
		MessageID: messageID,
		Source:    f.Source,
		Name:      f.Name,
		Mime:      f.Mime,
		Size:      f.Size,
		Sha256:    f.Sha256,
		CreatedAt: f.CreatedAt,
	}
}

type messageView struct {
	ID          string           `json:"id"`
	RoomID      string           `json:"room_id"`
	Role        string           `json:"role"`
	Body        string           `json:"body"`
	RunID       *string          `json:"run_id,omitempty"`
	Meta        json.RawMessage  `json:"meta,omitempty"`
	Attachments []attachmentView `json:"attachments"`
	CreatedAt   time.Time        `json:"created_at"`
}

func viewMessage(m store.Message, attachments []store.File) messageView {
	views := make([]attachmentView, 0, len(attachments))
	for _, f := range attachments {
		views = append(views, viewAttachment(f))
	}
	var meta json.RawMessage
	if len(m.Meta) > 0 && string(m.Meta) != "{}" && string(m.Meta) != "null" {
		meta = json.RawMessage(m.Meta)
	}
	return messageView{
		ID:          m.ID.String(),
		RoomID:      m.RoomID.String(),
		Role:        m.Role,
		Body:        m.Body,
		RunID:       m.RunID,
		Meta:        meta,
		Attachments: views,
		CreatedAt:   m.CreatedAt,
	}
}

func attachmentsByMessage(files []store.File) map[uuid.UUID][]store.File {
	out := make(map[uuid.UUID][]store.File)
	for _, f := range files {
		if f.MessageID != nil {
			out[*f.MessageID] = append(out[*f.MessageID], f)
		}
	}
	return out
}
