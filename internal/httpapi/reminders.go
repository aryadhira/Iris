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

	"github.com/aryadhira/iris/internal/reminder"
	"github.com/aryadhira/iris/internal/store"
)

const (
	reminderBatch   = 20
	reminderMaxText = 500
)

type reminderView struct {
	ID        string     `json:"id"`
	RoomID    string     `json:"room_id"`
	Text      string     `json:"text"`
	DueAt     time.Time  `json:"due_at"`
	State     string     `json:"state"`
	CreatedAt time.Time  `json:"created_at"`
	FiredAt   *time.Time `json:"fired_at,omitempty"`
}

func viewReminder(row store.Reminder) reminderView {
	return reminderView{
		ID:        row.ID.String(),
		RoomID:    row.RoomID.String(),
		Text:      row.Text,
		DueAt:     row.DueAt,
		State:     row.State,
		CreatedAt: row.CreatedAt,
		FiredAt:   row.FiredAt,
	}
}

type createReminderRequest struct {
	Text  string `json:"text"`
	DueAt string `json:"due_at"`
}

func (s *Server) createReminder(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	room, err := s.store.GetRoom(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for reminder", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room ini."))
	}
	if room.ArchivedAt != nil {
		return c.JSON(http.StatusConflict, errorBody("Room ini sudah diarsipkan."))
	}

	var req createReminderRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Tulis pengingat dan waktunya."))
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || len([]rune(text)) > reminderMaxText {
		return c.JSON(http.StatusBadRequest, errorBody("Teks pengingat harus 1–500 karakter."))
	}
	dueAt, err := time.Parse(time.RFC3339, strings.TrimSpace(req.DueAt))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Waktu pengingat itu tidak valid."))
	}

	row, err := s.store.CreateReminder(c.Request().Context(), store.CreateReminderParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Text:   text,
		DueAt:  dueAt,
	})
	if err != nil {
		s.logger.Error("cannot create reminder", "error", err, "room_id", room.ID.String())
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan pengingat ini."))
	}
	return c.JSON(http.StatusCreated, map[string]any{"reminder": viewReminder(row)})
}

func (s *Server) listReminders(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	if _, err := s.store.GetRoom(c.Request().Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for reminders", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room ini."))
	}
	rows, err := s.store.ListPendingReminders(c.Request().Context(), id)
	if err != nil {
		s.logger.Error("cannot list reminders", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memuat pengingat room ini."))
	}
	views := make([]reminderView, 0, len(rows))
	for _, row := range rows {
		views = append(views, viewReminder(row))
	}
	return c.JSON(http.StatusOK, map[string]any{"reminders": views})
}

func (s *Server) cancelReminder(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	reminderID, err := uuid.Parse(c.Param("rid"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errorBody("Pengingat itu tidak ada di room ini."))
	}
	row, err := s.store.CancelReminder(c.Request().Context(), store.CancelReminderParams{
		ID:     reminderID,
		RoomID: id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Pengingat itu tidak ada atau sudah berbunyi."))
		}
		s.logger.Error("cannot cancel reminder", "error", err, "room_id", id.String())
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membatalkan pengingat ini."))
	}
	return c.JSON(http.StatusOK, map[string]any{"reminder": viewReminder(row)})
}

// createParsedReminder is the typed-message path: parse Boss' sentence before
// dispatch, create only on an explicit time, and return the row so the SSE
// stream can show the small toast.
func (s *Server) createParsedReminder(ctx context.Context, room store.Room, text string) (*store.Reminder, bool) {
	parsed, ok := reminder.Parse(text, s.now(), s.reminderLoc)
	if !ok {
		return nil, false
	}
	row, err := s.store.CreateReminder(ctx, store.CreateReminderParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Text:   parsed.Text,
		DueAt:  parsed.DueAt,
	})
	if err != nil {
		s.logger.Error("cannot create parsed reminder", "error", err, "room_id", room.ID.String())
		return nil, false
	}
	s.logger.Info("reminder created from message", "room_id", room.ID.String(), "reminder_id", row.ID.String(), "due_at", row.DueAt.Format(time.RFC3339))
	return &row, true
}

// StartReminders sweeps immediately (so a boot catches up) and then every 30 s.
func (s *Server) StartReminders(ctx context.Context) {
	s.runDueReminders(ctx)
	go func() {
		ticker := time.NewTicker(s.reminderInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runDueReminders(ctx)
			}
		}
	}()
}

// runDueReminders is DB-derived: a restart re-reads pending rows. FireReminder
// updates the row and inserts the message in one statement, so the same row can
// never produce two visible reminders.
func (s *Server) runDueReminders(ctx context.Context) {
	now := s.now()
	rows, err := s.store.ListDueReminders(ctx, store.ListDueRemindersParams{
		DueAt:    now,
		RowLimit: reminderBatch,
	})
	if err != nil {
		s.logger.Error("cannot read due reminders", "error", err)
		return
	}
	for _, row := range rows {
		room, err := s.store.GetRoom(ctx, row.RoomID)
		if err != nil {
			s.logger.Error("reminder room is gone", "error", err, "room_id", row.RoomID.String())
			continue
		}
		if room.ArchivedAt != nil {
			if _, err := s.store.CancelReminder(ctx, store.CancelReminderParams{ID: row.ID, RoomID: room.ID}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				s.logger.Error("cannot cancel reminder in archived room", "error", err, "reminder_id", row.ID.String())
			}
			continue
		}

		body := "⏰ Pengingat: " + row.Text
		late := row.DueAt.Before(s.bootTime)
		if late {
			body += " (terlambat " + lateText(now.Sub(row.DueAt)) + ")"
		}
		firedAt := now
		message, err := s.store.FireReminder(ctx, store.FireReminderParams{
			ReminderID: row.ID,
			MessageID:  uuid.New(),
			Body:       body,
			FiredAt:    &firedAt,
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				s.logger.Error("cannot fire reminder", "error", err, "reminder_id", row.ID.String())
			}
			continue
		}
		s.logger.Info("reminder fired", "room_id", room.ID.String(), "reminder_id", row.ID.String(), "late", late)
		s.notifyPush(ctx, room, body, message.ID.String(), false)
	}
}

func lateText(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%d detik", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d menit", int(d.Minutes()))
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if minutes == 0 {
		return fmt.Sprintf("%d jam", hours)
	}
	return fmt.Sprintf("%d jam %d menit", hours, minutes)
}
