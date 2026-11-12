package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/hermes"
)

const stopTimeout = 15 * time.Second

// stopRoom interrupts every live run in a room. It is idempotent: a second
// call finds nothing active and answers 200 with a reason, never a 500.
func (s *Server) stopRoom(c echo.Context) error {
	roomID, err := roomID(c)
	if err != nil {
		return err
	}
	room, err := s.store.GetRoom(c.Request().Context(), roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room to stop", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room ini."))
	}

	ctx := context.WithoutCancel(c.Request().Context())
	runs, err := s.store.ListActiveRuns(ctx, room.ID)
	if err != nil {
		s.logger.Error("cannot list active runs", "error", err, "room_id", room.ID.String())
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memeriksa proses room ini."))
	}

	stopped := 0
	reasons := make([]string, 0, len(runs))
	for _, run := range runs {
		callCtx, cancel := context.WithTimeout(ctx, stopTimeout)
		status, stopErr := s.hermes.StopRun(callCtx, run.RunID)
		cancel()
		if stopErr != nil {
			var apiErr *hermes.APIError
			if errors.As(stopErr, &apiErr) &&
				(apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusConflict) {
				// Hermes does not know this run any more. Do not claim it was
				// cancelled: record that it is no longer live and move on.
				s.updateRunStatus(ctx, run.RunID, "interrupted", true)
				reasons = append(reasons, "run sudah tidak aktif di Hermes")
				continue
			}
			s.logger.Error("cannot stop run", "error", stopErr, "run_id", run.RunID, "room_id", room.ID.String())
			reasons = append(reasons, "Hermes tidak menjawab")
			continue
		}
		s.updateRunStatus(ctx, run.RunID, "stopping", false)
		stopped++
		if status.Status != "" {
			reasons = append(reasons, status.Status)
		}
	}

	// A stop is a decision, not a pause: kill pending follow-ups too.
	cancelled := s.cancelRoomContinuations(ctx, room.ID, "dihentikan oleh Boss", "")
	if stopped > 0 || cancelled > 0 {
		s.noteSystem(ctx, room.ID, "dihentikan oleh Boss")
	}

	reason := strings.Join(reasons, "; ")
	if stopped == 0 && cancelled > 0 && reason == "" {
		reason = "lanjutan otomatis dibatalkan"
	}
	if stopped == 0 && reason == "" {
		reason = "tidak ada yang berjalan"
	}
	return c.JSON(http.StatusOK, map[string]any{"stopped": stopped, "reason": reason})
}
