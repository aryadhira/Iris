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
	"github.com/aryadhira/iris/internal/live"
	"github.com/aryadhira/iris/internal/pricing"
	"github.com/aryadhira/iris/internal/reminder"
	"github.com/aryadhira/iris/internal/store"
	"github.com/aryadhira/iris/internal/webpush"
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
	StartStreamingMessage(ctx context.Context, arg store.StartStreamingMessageParams) (store.Message, error)
	UpdateMessagePartial(ctx context.Context, arg store.UpdateMessagePartialParams) error
	SetMessageRunID(ctx context.Context, arg store.SetMessageRunIDParams) error
	FinalizeMessage(ctx context.Context, arg store.FinalizeMessageParams) (store.Message, error)
	DeleteStreamingMessage(ctx context.Context, id uuid.UUID) error
	GetStreamingMessage(ctx context.Context, roomID uuid.UUID) (store.Message, error)
	MarkStreamingMessagesInterrupted(ctx context.Context) ([]store.Message, error)
	ListMessages(ctx context.Context, arg store.ListMessagesParams) ([]store.Message, error)
	ListAllMessages(ctx context.Context, roomID uuid.UUID) ([]store.Message, error)
	GetMessage(ctx context.Context, id uuid.UUID) (store.Message, error)
	GetLatestAssistantMessage(ctx context.Context, roomID uuid.UUID) (store.Message, error)
	ListMessagesAfter(ctx context.Context, arg store.ListMessagesAfterParams) ([]store.Message, error)
	MarkMessageSuperseded(ctx context.Context, id uuid.UUID) (store.Message, error)
	SearchMessages(ctx context.Context, arg store.SearchMessagesParams) ([]store.SearchMessagesRow, error)

	CreateContinuation(ctx context.Context, arg store.CreateContinuationParams) (store.Continuation, error)
	GetContinuation(ctx context.Context, id uuid.UUID) (store.Continuation, error)
	ListPendingContinuations(ctx context.Context, roomID uuid.UUID) ([]store.Continuation, error)
	ListOpenContinuations(ctx context.Context) ([]store.Continuation, error)
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

	UpsertRun(ctx context.Context, arg store.UpsertRunParams) (store.Run, error)
	UpdateRunStatus(ctx context.Context, arg store.UpdateRunStatusParams) (store.Run, error)
	GetRun(ctx context.Context, runID string) (store.Run, error)
	GetLatestRun(ctx context.Context, roomID uuid.UUID) (store.Run, error)
	ListLatestRuns(ctx context.Context) ([]store.Run, error)
	ListActiveRuns(ctx context.Context, roomID uuid.UUID) ([]store.Run, error)
	MarkActiveRunsInterrupted(ctx context.Context, updatedAt time.Time) ([]store.Run, error)
	MarkStaleRunsInterrupted(ctx context.Context, cutoff time.Time) ([]store.Run, error)

	UpsertApproval(ctx context.Context, arg store.UpsertApprovalParams) (store.Approval, error)
	GetApproval(ctx context.Context, id uuid.UUID) (store.Approval, error)
	ListApprovals(ctx context.Context, roomID uuid.UUID) ([]store.Approval, error)
	ResolveApproval(ctx context.Context, arg store.ResolveApprovalParams) (store.Approval, error)
	ExpireApprovals(ctx context.Context, resolvedAt *time.Time) ([]store.Approval, error)
	CountPendingApprovalsByRoom(ctx context.Context, expiresAt time.Time) ([]store.CountPendingApprovalsByRoomRow, error)

	CreateReminder(ctx context.Context, arg store.CreateReminderParams) (store.Reminder, error)
	ListPendingReminders(ctx context.Context, roomID uuid.UUID) ([]store.Reminder, error)
	CancelReminder(ctx context.Context, arg store.CancelReminderParams) (store.Reminder, error)
	ListDueReminders(ctx context.Context, arg store.ListDueRemindersParams) ([]store.Reminder, error)
	FireReminder(ctx context.Context, arg store.FireReminderParams) (store.Message, error)

	ListPushSubscriptions(ctx context.Context) ([]store.PushSubscription, error)
	UpsertPushSubscription(ctx context.Context, arg store.UpsertPushSubscriptionParams) (store.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, endpoint string) (int64, error)

	InsertUsage(ctx context.Context, arg store.InsertUsageParams) (store.Usage, error)
	UsageTotals(ctx context.Context, arg store.UsageTotalsParams) (store.UsageTotalsRow, error)
	UsageDaily(ctx context.Context, arg store.UsageDailyParams) ([]store.UsageDailyRow, error)
	UsageByRoom(ctx context.Context, arg store.UsageByRoomParams) ([]store.UsageByRoomRow, error)
	UsageSpendBetween(ctx context.Context, arg store.UsageSpendBetweenParams) (store.UsageSpendBetweenRow, error)
	InsertBudgetOverride(ctx context.Context, arg store.InsertBudgetOverrideParams) (store.BudgetOverride, error)
}

// PushNotifier is the seam for Web Push. The real implementation lives in
// internal/webpush; tests use a recorder.
type PushNotifier interface {
	PublicKey() string
	Notify(ctx context.Context, roomID uuid.UUID, payload webpush.Payload, exempt bool) (int, error)
}

// Server wires the HTTP surface.
type Server struct {
	store   Store
	hermes  hermes.Client
	voice   VoiceClient
	auth    *auth.Service
	logger  *slog.Logger
	version string
	dataDir string

	// now and continuationInterval are seams for the pump's tests: a fake
	// clock must be able to cross the 30 minute cap without sleeping.
	now                  func() time.Time
	continuationInterval time.Duration

	hub             *live.Hub
	approvalTimeout time.Duration
	approvalFixture bool

	// partialInterval/partialChars decide when a streaming assistant row is
	// written to Postgres. Both are seams for tests.
	partialInterval time.Duration
	partialChars    int

	schemaVersion    string
	push             PushNotifier
	reminderLoc      *time.Location
	reminderInterval time.Duration
	// prices is the editable price table. A missing file means "cost unknown",
	// never "cost zero".
	prices      *pricing.Table
	dailyBudget float64
	// bootTime distinguishes "the scheduler is late by one tick" from "the
	// due time passed while irisd was down".
	bootTime time.Time
}

// Options configures a Server.
type Options struct {
	Store                Store
	Hermes               hermes.Client
	Voice                VoiceClient
	Auth                 *auth.Service
	Logger               *slog.Logger
	Version              string
	DataDir              string
	WebDir               string
	Now                  func() time.Time
	ContinuationInterval time.Duration
	RunContinuations     bool
	ApprovalTimeout      time.Duration
	RunApprovals         bool
	ApprovalFixture      bool
	SchemaVersion        string
	Push                 PushNotifier
	RunReminders         bool
	ReminderInterval     time.Duration
	Prices               *pricing.Table
	DailyBudgetUSD       float64
	PartialInterval      time.Duration
	PartialChars         int
}

// New builds the Echo application with all routes registered.
func New(opts Options) *echo.Echo {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		store:           opts.Store,
		hermes:          opts.Hermes,
		voice:           opts.Voice,
		auth:            opts.Auth,
		logger:          logger,
		version:         opts.Version,
		dataDir:         opts.DataDir,
		now:             opts.Now,
		hub:             live.New(),
		approvalTimeout: opts.ApprovalTimeout,
		approvalFixture: opts.ApprovalFixture,
		schemaVersion:   opts.SchemaVersion,
		push:            opts.Push,
		reminderLoc:     reminder.Jakarta(),
		prices:          opts.Prices,
		dailyBudget:     opts.DailyBudgetUSD,
	}
	if s.prices == nil {
		s.prices = pricing.New("")
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.bootTime = s.now()
	s.continuationInterval = opts.ContinuationInterval
	if s.continuationInterval == 0 {
		s.continuationInterval = time.Second
	}
	s.reminderInterval = opts.ReminderInterval
	if s.reminderInterval == 0 {
		s.reminderInterval = 30 * time.Second
	}
	s.partialInterval = opts.PartialInterval
	if s.partialInterval == 0 {
		s.partialInterval = 2 * time.Second
	}
	s.partialChars = opts.PartialChars
	if s.partialChars == 0 {
		s.partialChars = 400
	}

	// A row still marked streaming can only be the remains of a process that
	// died mid-turn. Mark it before anything reads the room.
	s.recoverInterrupted(context.Background())
	s.startRunReaper(context.Background())

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
	e.GET("/api/rooms/:id/events", s.roomEvents)
	e.POST("/api/rooms/:id/messages", s.streamMessage)
	e.POST("/api/rooms/:id/messages/:mid/retry", s.retryMessage)
	e.GET("/api/rooms/:id/export.md", s.exportRoom)
	e.GET("/api/rooms/:id/continuation", s.getContinuation)
	e.POST("/api/rooms/:id/continuation/cancel", s.cancelContinuation)
	e.GET("/api/rooms/:id/approvals", s.listApprovals)
	e.POST("/api/rooms/:id/approvals/:approvalId", s.resolveApproval)
	e.POST("/api/rooms/:id/stop", s.stopRoom)
	e.GET("/api/rooms/:id/reminders", s.listReminders)
	e.POST("/api/rooms/:id/reminders", s.createReminder)
	e.DELETE("/api/rooms/:id/reminders/:rid", s.cancelReminder)
	if opts.ApprovalFixture {
		e.POST("/api/rooms/:id/approvals/fixture", s.fixtureApproval)
	}
	e.GET("/api/rooms/:id/files", s.listRoomFiles)
	e.POST("/api/rooms/:id/uploads", s.uploadFile)

	e.GET("/api/push/key", s.pushKey)
	e.POST("/api/push/subscribe", s.subscribePush)
	e.DELETE("/api/push/subscribe", s.unsubscribePush)

	e.POST("/api/voice/transcribe", s.voiceTranscribe)
	e.POST("/api/voice/speak", s.voiceSpeak)
	e.POST("/api/voice/speak/stream", s.voiceSpeakStream)

	e.GET("/api/files/:id", s.downloadFile)
	e.DELETE("/api/files/:id", s.deleteFile)

	e.GET("/api/search", s.searchMessages)
	e.GET("/api/usage", s.getUsage)

	// Unknown API paths answer JSON; they must never fall through to the SPA
	// history handler and return index.html.
	e.Any("/api", apiNotFound)
	e.Any("/api/*", apiNotFound)

	s.registerWeb(e, opts.WebDir)

	if opts.RunContinuations {
		s.StartContinuations(context.Background())
	}
	if opts.RunApprovals {
		s.StartApprovals(context.Background())
	}
	if opts.RunReminders {
		s.StartReminders(context.Background())
	}

	return e
}

func (s *Server) health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"ok":      true,
		"version": s.version,
		"schema":  s.schemaVersion,
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
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Topic            string     `json:"topic"`
	PendingApprovals int64      `json:"pending_approvals"`
	Run              *runView   `json:"run"`
	CreatedAt        time.Time  `json:"created_at"`
	ArchivedAt       *time.Time `json:"archived_at,omitempty"`
}

// runView is the cold-load truth: enough for a working dot and a clock without
// opening any stream. continuation is the attempt number of a live follow-up
// (0 for a normal turn), chain its root message id.
type runView struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"started_at"`
	Continuation int32     `json:"continuation"`
	Chain        string    `json:"chain,omitempty"`
}

func runViewOf(row *store.Run) *runView {
	if row == nil {
		return nil
	}
	return &runView{
		ID:        row.RunID,
		Status:    row.Status,
		StartedAt: row.StartedAt,
	}
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
	State       string           `json:"state"`
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
		State:       m.State,
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
