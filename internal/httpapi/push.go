package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/store"
	"github.com/aryadhira/iris/internal/webpush"
)

type pushSubscribeRequest struct {
	RoomID   string `json:"room_id"`
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

type pushSubscriptionView struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func viewPushSubscription(row store.PushSubscription) pushSubscriptionView {
	return pushSubscriptionView{
		ID:        row.ID.String(),
		RoomID:    row.RoomID.String(),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func (s *Server) pushKey(c echo.Context) error {
	if s.push == nil {
		return c.JSON(http.StatusServiceUnavailable, errorBody("Notifikasi belum disiapkan di server ini."))
	}
	return c.JSON(http.StatusOK, map[string]string{"public_key": s.push.PublicKey()})
}

// subscribePush stores one device subscription. The room is checked through
// the same owner-scoped store as every other room route, so a subscription can
// never be attached to a room this owner cannot open.
func (s *Server) subscribePush(c echo.Context) error {
	var req pushSubscribeRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Langganan notifikasi itu tidak valid."))
	}
	roomID, err := uuid.Parse(strings.TrimSpace(req.RoomID))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Room untuk notifikasi ini tidak valid."))
	}
	if _, err := s.store.GetRoom(c.Request().Context(), roomID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for push subscription", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room ini."))
	}

	endpoint := strings.TrimSpace(req.Endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return c.JSON(http.StatusBadRequest, errorBody("Endpoint notifikasi itu tidak valid."))
	}
	p256dh := strings.TrimSpace(req.Keys.P256dh)
	auth := strings.TrimSpace(req.Keys.Auth)
	if p256dh == "" || auth == "" {
		return c.JSON(http.StatusBadRequest, errorBody("Kunci notifikasi itu tidak lengkap."))
	}

	row, err := s.store.UpsertPushSubscription(c.Request().Context(), store.UpsertPushSubscriptionParams{
		ID:       uuid.New(),
		RoomID:   roomID,
		Endpoint: endpoint,
		P256dh:   p256dh,
		Auth:     auth,
	})
	if err != nil {
		s.logger.Error("cannot store push subscription", "error", err, "room_id", roomID.String())
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan langganan notifikasi."))
	}
	return c.JSON(http.StatusCreated, map[string]any{"subscription": viewPushSubscription(row)})
}

// notifyPush is best-effort: a push failure is logged, never allowed to undo a
// reminder, an approval or a finished turn.
func (s *Server) notifyPush(ctx context.Context, room store.Room, text, messageID string, exempt bool) {
	if s.push == nil {
		return
	}
	payload := webpush.NewPayload(room.Name, text, room.ID.String(), messageID)
	sent, err := s.push.Notify(ctx, room.ID, payload, exempt)
	if err != nil {
		s.logger.Warn("push not delivered", "error", err, "room_id", room.ID.String(), "sent", sent)
	}
}

// notifyApproval is never debounced: the run is blocked until Boss answers.
func (s *Server) notifyApproval(ctx context.Context, room store.Room, row store.Approval) {
	text := "Butuh izin"
	if row.Description != "" {
		text += ": " + row.Description
	}
	if row.Command != "" {
		text += " — " + row.Command
	}
	s.notifyPush(ctx, room, text, "", true)
}

func (s *Server) unsubscribePush(c echo.Context) error {
	endpoint := strings.TrimSpace(c.QueryParam("endpoint"))
	if endpoint == "" {
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		// A DELETE with no body is normal; only a malformed body is a 400.
		if err := c.Bind(&req); err == nil {
			endpoint = strings.TrimSpace(req.Endpoint)
		}
	}
	if endpoint == "" {
		return c.JSON(http.StatusBadRequest, errorBody("Endpoint notifikasi itu tidak valid."))
	}
	deleted, err := s.store.DeletePushSubscription(c.Request().Context(), endpoint)
	if err != nil {
		s.logger.Error("cannot remove push subscription", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menghapus langganan notifikasi."))
	}
	return c.JSON(http.StatusOK, map[string]any{"deleted": deleted})
}
