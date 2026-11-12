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

	"github.com/aryadhira/iris/internal/rooms"
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

	// The room folder exists before Hermes is called, so the prompt can name the
	// real absolute path.
	folder, err := rooms.Create(s.dataDir, roomID)
	if err != nil {
		s.logger.Error("cannot create room folder", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyiapkan folder room. Tidak ada room yang dibuat."))
	}
	prompt := folder.SystemPrompt()

	// Hermes first: a room without a session is useless, and a failed session
	// must not leave a row behind. The call is detached from the request so a
	// phone that drops mid-create cannot strand an orphan session.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 15*time.Second)
	sessionID, err := s.hermes.CreateSession(ctx, title, prompt)
	cancel()
	if err != nil {
		_ = folder.Remove()
		s.logger.Error("cannot create hermes session", "error", err)
		return c.JSON(http.StatusBadGateway, errorBody("Hermes tidak menjawab. Tidak ada room yang dibuat."))
	}

	row, err := s.store.CreateRoom(c.Request().Context(), store.CreateRoomParams{
		ID:              roomID,
		Name:            name,
		Topic:           topic,
		HermesSessionID: sessionID,
		FolderPath:      folder.Root,
		SystemPrompt:    prompt,
	})
	if err != nil {
		_ = folder.Remove()
		s.logger.Error("cannot save room; cleaning up hermes session", "error", err, "hermes_session_id", sessionID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if derr := s.hermes.DeleteSession(cleanupCtx, sessionID); derr != nil {
			s.logger.Error("cannot clean up orphan hermes session", "error", derr, "hermes_session_id", sessionID)
		}
		cleanupCancel()
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan room. Tidak ada yang dibuat."))
	}

	return c.JSON(http.StatusCreated, map[string]any{"room": viewRoom(row)})
}

func (s *Server) listRooms(c echo.Context) error {
	ctx := c.Request().Context()
	s.expireApprovals(ctx)
	rows, err := s.store.ListRooms(ctx)
	if err != nil {
		s.logger.Error("cannot list rooms", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris could not load your rooms."))
	}
	counts, err := s.store.CountPendingApprovalsByRoom(ctx, s.now())
	if err != nil {
		s.logger.Error("cannot count pending approvals", "error", err)
	}
	byRoom := make(map[uuid.UUID]int64, len(counts))
	for _, row := range counts {
		byRoom[row.RoomID] = row.Count
	}
	runs := make(map[uuid.UUID]*store.Run)
	if latest, err := s.store.ListLatestRuns(ctx); err != nil {
		s.logger.Error("cannot list latest runs", "error", err)
	} else {
		for i := range latest {
			runs[latest[i].RoomID] = activeRun(&latest[i])
		}
	}
	chains := make(map[uuid.UUID]store.Continuation)
	if open, err := s.store.ListOpenContinuations(ctx); err != nil {
		s.logger.Error("cannot list open continuations", "error", err)
	} else {
		for _, row := range open {
			if _, exists := chains[row.RoomID]; !exists {
				chains[row.RoomID] = row
			}
		}
	}
	rooms := make([]roomView, 0, len(rows))
	for _, row := range rows {
		view := viewRoom(row)
		view.PendingApprovals = byRoom[row.ID]
		applyRun(&view, runs[row.ID], chains[row.ID])
		rooms = append(rooms, view)
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
	s.expireApprovals(c.Request().Context())
	view := viewRoom(row)
	if counts, err := s.store.CountPendingApprovalsByRoom(c.Request().Context(), s.now()); err == nil {
		for _, count := range counts {
			if count.RoomID == row.ID {
				view.PendingApprovals = count.Count
			}
		}
	}
	var latest *store.Run
	if run, err := s.store.GetLatestRun(c.Request().Context(), row.ID); err == nil {
		latest = &run
	}
	var chain store.Continuation
	if rows, err := s.store.ListPendingContinuations(c.Request().Context(), row.ID); err == nil && len(rows) > 0 {
		chain = rows[0]
	}
	applyRun(&view, latest, chain)
	return c.JSON(http.StatusOK, map[string]any{"room": view})
}

// applyRun fills the cold-load run and continuation index. An open follow-up
// exists even while no run is live, which is exactly "menunggu proses".
func applyRun(view *roomView, run *store.Run, chain store.Continuation) {
	view.Run = runViewOf(activeRun(run))
	if view.Run == nil || chain.ID == uuid.Nil {
		return
	}
	view.Run.Continuation = chain.Attempt
	view.Run.Chain = chain.ChainID.String()
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
	s.cancelRoomContinuations(c.Request().Context(), row.ID, "room diarsipkan", "lanjut otomatis dibatalkan — room diarsipkan.")
	return c.JSON(http.StatusOK, map[string]any{"room": viewRoom(row)})
}

type updatePromptRequest struct {
	SystemPrompt string `json:"system_prompt"`
}

// updateRoomPrompt changes the contract prepended to every future turn. The
// prompt is sent as per-turn instructions because this Hermes build ignores a
// session's stored system_prompt during chat.
func (s *Server) updateRoomPrompt(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	var req updatePromptRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Kirim system_prompt sebagai teks."))
	}
	prompt := strings.TrimSpace(req.SystemPrompt)
	if prompt == "" || len(prompt) > 8000 {
		return c.JSON(http.StatusBadRequest, errorBody("Prompt room tidak boleh kosong atau lebih dari 8.000 karakter."))
	}
	row, err := s.store.UpdateRoomPrompt(c.Request().Context(), store.UpdateRoomPromptParams{
		ID:           id,
		SystemPrompt: prompt,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot update room prompt", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan prompt room ini."))
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
