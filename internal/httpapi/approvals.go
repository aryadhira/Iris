package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/live"
	"github.com/aryadhira/iris/internal/store"
)

const (
	defaultApprovalTTL = 5 * time.Minute
	approvalSweep      = 2 * time.Second
)

// approvalView is the only shape the browser sees. request_id and the Hermes
// session id stay server-side; run_id belongs to the message inspector.
type approvalView struct {
	ID          string     `json:"id"`
	RoomID      string     `json:"room_id"`
	ToolName    string     `json:"tool_name,omitempty"`
	Command     string     `json:"command"`
	Description string     `json:"description,omitempty"`
	Choices     []string   `json:"choices"`
	State       string     `json:"state"`
	Choice      *string    `json:"choice,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
}

func viewApproval(a store.Approval) approvalView {
	choices := a.Choices
	if choices == nil {
		choices = []string{}
	}
	return approvalView{
		ID:          a.ID.String(),
		RoomID:      a.RoomID.String(),
		ToolName:    a.ToolName,
		Command:     a.Command,
		Description: a.Description,
		Choices:     choices,
		State:       a.State,
		Choice:      a.Choice,
		CreatedAt:   a.CreatedAt,
		ExpiresAt:   a.ExpiresAt,
		ResolvedAt:  a.ResolvedAt,
	}
}

func (s *Server) approvalTTL() time.Duration {
	if s.approvalTimeout > 0 {
		return s.approvalTimeout
	}
	return defaultApprovalTTL
}

// recordApproval persists one approval exactly as Hermes sent it. The command
// is already redacted by the gateway; irisd never reconstructs or logs it.
func (s *Server) recordApproval(ctx context.Context, room store.Room, runID string, ev hermes.Event, fallbackSession string) (*store.Approval, error) {
	if ev.RequestID == "" {
		return nil, errors.New("approval event carried no request_id")
	}
	sessionID := ev.SessionID
	if sessionID == "" {
		sessionID = fallbackSession
	}
	now := s.now()
	row, err := s.store.UpsertApproval(ctx, store.UpsertApprovalParams{
		ID:          uuid.New(),
		RoomID:      room.ID,
		RunID:       runID,
		SessionID:   sessionID,
		RequestID:   ev.RequestID,
		ToolName:    ev.Name,
		Command:     ev.Command,
		Description: ev.Description,
		Choices:     append([]string(nil), ev.Choices...),
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.approvalTTL()),
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// expireApprovals is both the sweeper and the read-side guard: a stale pending
// row must never look answerable, even if the ticker has not run yet.
func (s *Server) expireApprovals(ctx context.Context) []store.Approval {
	now := s.now()
	rows, err := s.store.ExpireApprovals(ctx, &now)
	if err != nil {
		s.logger.Error("cannot expire approvals", "error", err)
		return nil
	}
	for _, row := range rows {
		s.hub.Publish(row.RoomID, live.Event{Name: "approval.resolved", Payload: viewApproval(row)})
	}
	return rows
}

// StartApprovals expires parked approvals while no HTTP request is reading.
func (s *Server) StartApprovals(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(approvalSweep)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.expireApprovals(ctx)
			}
		}
	}()
}

func (s *Server) listApprovals(c echo.Context) error {
	roomID, err := roomID(c)
	if err != nil {
		return err
	}
	if _, err := s.store.GetRoom(c.Request().Context(), roomID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for approvals", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room ini."))
	}
	s.expireApprovals(c.Request().Context())
	rows, err := s.store.ListApprovals(c.Request().Context(), roomID)
	if err != nil {
		s.logger.Error("cannot list approvals", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memuat izin room ini."))
	}
	views := make([]approvalView, 0, len(rows))
	for _, row := range rows {
		views = append(views, viewApproval(row))
	}
	return c.JSON(http.StatusOK, map[string]any{"approvals": views})
}

type resolveApprovalRequest struct {
	Choice string `json:"choice"`
}

func (s *Server) resolveApproval(c echo.Context) error {
	roomID, err := roomID(c)
	if err != nil {
		return err
	}
	approvalID, err := uuid.Parse(c.Param("approvalId"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errorBody("Izin itu tidak ada di room ini."))
	}
	var req resolveApprovalRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Pilih izin atau tolak dulu."))
	}
	choice := strings.ToLower(strings.TrimSpace(req.Choice))
	if choice == "" {
		return c.JSON(http.StatusBadRequest, errorBody("Pilih izin atau tolak dulu."))
	}

	ctx := c.Request().Context()
	if _, err := s.store.GetRoom(ctx, roomID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for approval", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room ini."))
	}
	s.expireApprovals(ctx)

	row, err := s.store.GetApproval(ctx, approvalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Izin itu tidak ada di room ini."))
		}
		s.logger.Error("cannot load approval", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka izin ini."))
	}
	if row.RoomID != roomID {
		return c.JSON(http.StatusNotFound, errorBody("Izin itu tidak ada di room ini."))
	}
	if row.State != "pending" {
		return c.JSON(http.StatusConflict, errorBody(approvalGoneMessage(row.State)))
	}
	if !s.now().Before(row.ExpiresAt) {
		_, _ = s.store.ResolveApproval(ctx, store.ResolveApprovalParams{
			ID:         row.ID,
			State:      "expired",
			ResolvedAt: timePtr(s.now()),
		})
		return c.JSON(http.StatusConflict, errorBody("Izinnya sudah tidak berlaku."))
	}
	if !containsChoice(row.Choices, choice) {
		return c.JSON(http.StatusBadRequest, errorBody("Pilihan itu tidak ditawarkan oleh Hermes untuk izin ini."))
	}

	// Fixture approvals exist only in the browser-test stack. They resolve
	// locally so the card can be exercised without touching a live run.
	if s.approvalFixture && strings.HasPrefix(row.RunID, "run_fixture_") {
		return s.resolveFixtureApproval(c, row, choice)
	}

	if err := s.hermes.ResolveApproval(ctx, row.RunID, row.RequestID, choice); err != nil {
		var apiErr *hermes.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			s.supersedeApproval(ctx, row, "hermes_409_"+apiErr.Code())
			return c.JSON(http.StatusConflict, errorBody("Izinnya sudah tidak berlaku."))
		}
		s.logger.Error("cannot resolve approval with Hermes", "error", err, "approval_id", row.ID.String())
		return c.JSON(http.StatusBadGateway, errorBody("Hermes tidak menjawab. Izin ini belum tercatat sebagai dijawab."))
	}

	state := "allowed"
	if choice == "deny" {
		state = "denied"
	}
	updated, err := s.store.ResolveApproval(ctx, store.ResolveApprovalParams{
		ID:         row.ID,
		State:      state,
		Choice:     &choice,
		ResolvedAt: timePtr(s.now()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusConflict, errorBody("Izin ini sudah dijawab."))
		}
		s.logger.Error("cannot store approval decision", "error", err, "approval_id", row.ID.String())
		return c.JSON(http.StatusInternalServerError, errorBody("Hermes sudah menjawab, tapi Iris tidak bisa menyimpan keputusanmu."))
	}
	view := viewApproval(updated)
	s.hub.Publish(row.RoomID, live.Event{Name: "approval.resolved", Payload: view})
	return c.JSON(http.StatusOK, map[string]any{"approval": view})
}

func (s *Server) resolveFixtureApproval(c echo.Context, row store.Approval, choice string) error {
	state := "allowed"
	if choice == "deny" {
		state = "denied"
	}
	updated, err := s.store.ResolveApproval(c.Request().Context(), store.ResolveApprovalParams{
		ID:         row.ID,
		State:      state,
		Choice:     &choice,
		ResolvedAt: timePtr(s.now()),
	})
	if err != nil {
		return c.JSON(http.StatusConflict, errorBody("Izin ini sudah dijawab."))
	}
	view := viewApproval(updated)
	s.hub.Publish(row.RoomID, live.Event{Name: "approval.resolved", Payload: view})
	return c.JSON(http.StatusOK, map[string]any{"approval": view})
}

func (s *Server) supersedeApproval(ctx context.Context, row store.Approval, reason string) {
	updated, err := s.store.ResolveApproval(ctx, store.ResolveApprovalParams{
		ID:         row.ID,
		State:      "superseded",
		ResolvedAt: timePtr(s.now()),
	})
	if err != nil {
		s.logger.Error("cannot supersede approval", "error", err, "approval_id", row.ID.String(), "reason", reason)
		return
	}
	s.hub.Publish(row.RoomID, live.Event{Name: "approval.resolved", Payload: viewApproval(updated)})
}

func approvalGoneMessage(state string) string {
	switch state {
	case "denied":
		return "Izin ini sudah ditolak."
	case "allowed":
		return "Izin ini sudah diizinkan."
	case "expired":
		return "Izinnya sudah tidak berlaku."
	case "superseded":
		return "Izinnya sudah tidak berlaku."
	default:
		return "Izin ini sudah dijawab."
	}
}

func containsChoice(choices []string, want string) bool {
	for _, choice := range choices {
		if choice == want {
			return true
		}
	}
	return false
}

func timePtr(t time.Time) *time.Time { return &t }

// runWasDenied reports whether any approval for this run ended without consent.
// A refusal is a decision: the continuation pump must not re-run the turn and
// invite the same command again.
func (s *Server) runWasDenied(ctx context.Context, roomID uuid.UUID, runID string) bool {
	if runID == "" {
		return false
	}
	rows, err := s.store.ListApprovals(ctx, roomID)
	if err != nil {
		s.logger.Warn("cannot inspect approvals before continuation", "error", err, "room_id", roomID.String())
		return false
	}
	for _, row := range rows {
		if row.RunID != runID {
			continue
		}
		if row.State == "denied" || row.State == "expired" {
			return true
		}
	}
	return false
}

// fixtureApproval injects a synthetic approval through the same room event
// path a real approval uses. It is only registered when IRIS_APPROVAL_FIXTURE
// is set, and it never talks to Hermes.
type fixtureApprovalRequest struct {
	Command     string   `json:"command"`
	ToolName    string   `json:"tool_name"`
	Description string   `json:"description"`
	Choices     []string `json:"choices"`
	TTLSeconds  int      `json:"ttl_seconds"`
}

func (s *Server) fixtureApproval(c echo.Context) error {
	roomID, err := roomID(c)
	if err != nil {
		return err
	}
	room, err := s.store.GetRoom(c.Request().Context(), roomID)
	if err != nil {
		return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
	}
	var req fixtureApprovalRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Fixture tidak valid."))
	}
	if strings.TrimSpace(req.Command) == "" {
		req.Command = "echo fixture"
	}
	if len(req.Choices) == 0 {
		req.Choices = []string{"once", "session", "always", "deny"}
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = s.approvalTTL()
	}
	fixture := fmt.Sprintf("%d", s.now().UnixNano())
	now := s.now()
	row, err := s.store.UpsertApproval(c.Request().Context(), store.UpsertApprovalParams{
		ID:          uuid.New(),
		RoomID:      room.ID,
		RunID:       "run_fixture_" + fixture,
		SessionID:   room.HermesSessionID,
		RequestID:   "req_fixture_" + fixture,
		ToolName:    req.ToolName,
		Command:     req.Command,
		Description: req.Description,
		Choices:     req.Choices,
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
	})
	if err != nil {
		s.logger.Error("cannot store fixture approval", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Fixture approval gagal disimpan."))
	}
	view := viewApproval(row)
	s.hub.Publish(room.ID, live.Event{Name: "approval", Payload: view})
	return c.JSON(http.StatusCreated, map[string]any{"approval": view})
}
