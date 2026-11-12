package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/reply"
	"github.com/aryadhira/iris/internal/store"
)

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 50
)

type searchResultView struct {
	RoomID    string    `json:"roomId"`
	RoomName  string    `json:"roomName"`
	MessageID string    `json:"messageId"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
	Snippet   string    `json:"snippet"`
}

// searchMessages answers full-text search over the message mirror. Postgres
// does the ranking and the highlighting; irisd never re-implements tokenising.
func (s *Server) searchMessages(c echo.Context) error {
	query := strings.TrimSpace(c.QueryParam("q"))
	if query == "" {
		return c.JSON(http.StatusBadRequest, errorBody("Tulis kata yang ingin dicari dulu."))
	}
	if len(query) > 200 {
		return c.JSON(http.StatusBadRequest, errorBody("Kata pencarian terlalu panjang."))
	}

	limit := int32(defaultSearchLimit)
	if raw := c.QueryParam("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return c.JSON(http.StatusBadRequest, errorBody("Limit must be a whole number between 1 and 50."))
		}
		if n > maxSearchLimit {
			n = maxSearchLimit
		}
		limit = int32(n)
	}

	rows, err := s.store.SearchMessages(c.Request().Context(), store.SearchMessagesParams{
		Query:    query,
		RowLimit: limit,
	})
	if err != nil {
		s.logger.Error("cannot search messages", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa mencari sekarang. Coba lagi."))
	}

	results := make([]searchResultView, 0, len(rows))
	for _, row := range rows {
		results = append(results, searchResultView{
			RoomID:    row.RoomID.String(),
			RoomName:  row.RoomName,
			MessageID: row.ID.String(),
			Role:      row.Role,
			CreatedAt: row.CreatedAt,
			Snippet:   reply.CleanSnippet(string(row.Snippet)),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"results": results})
}
