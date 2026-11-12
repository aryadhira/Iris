package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/pricing"
	"github.com/aryadhira/iris/internal/reminder"
	"github.com/aryadhira/iris/internal/store"
)

// usageDay is the Jakarta-day label used by the chip, the chart and the budget
// guard. The same location is used everywhere so "today" cannot mean two
// different days on one screen.
func jakartaDay(t time.Time) time.Time {
	local := t.In(reminder.Jakarta())
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, reminder.Jakarta())
}

// recordUsage stores one row per assistant turn. A terminal payload without a
// usage object is still recorded — with zeros and usage_reported=false — because
// a missing counter must not look like a free turn.
func (s *Server) recordUsage(
	ctx context.Context,
	room store.Room,
	outcome turnOutcome,
	messageID *uuid.UUID,
) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()

	counters := pricing.Counters{}
	reported := outcome.usage != nil
	cacheReported := false
	if outcome.usage != nil {
		counters = pricing.Counters{
			Input:      outcome.usage.InputTokens,
			Output:     outcome.usage.OutputTokens,
			Total:      outcome.usage.TotalTokens,
			CacheRead:  outcome.usage.CacheReadTokens,
			CacheWrite: outcome.usage.CacheWriteTokens,
		}
		cacheReported = outcome.usage.CacheReported
	}

	var cost *float64
	if reported {
		if value, ok := s.prices.Cost(outcome.runtime.Provider, outcome.runtime.Model, counters); ok {
			cost = &value
		}
	}

	runID := outcome.runID
	if runID == "" {
		runID = "run-unknown-" + uuid.NewString()
	}
	row, err := s.store.InsertUsage(writeCtx, store.InsertUsageParams{
		ID:               uuid.New(),
		RunID:            runID,
		RoomID:           room.ID,
		MessageID:        messageID,
		Provider:         outcome.runtime.Provider,
		Model:            outcome.runtime.Model,
		InputTokens:      counters.Input,
		OutputTokens:     counters.Output,
		TotalTokens:      counters.Total,
		CacheReadTokens:  counters.CacheRead,
		CacheWriteTokens: counters.CacheWrite,
		CostUsd:          cost,
		UsageReported:    reported,
		CacheReported:    cacheReported,
		CreatedAt:        s.now(),
	})
	if err != nil {
		s.logger.Error("cannot record usage", "error", err, "room_id", room.ID.String(), "run_id", outcome.runID)
		return
	}
	var costValue any
	if row.CostUsd != nil {
		costValue = *row.CostUsd
	}
	s.logger.Info(
		"usage recorded",
		"room_id", room.ID.String(),
		"run_id", row.RunID,
		"provider", row.Provider,
		"model", row.Model,
		"total_tokens", row.TotalTokens,
		"reported", row.UsageReported,
		"cost_usd", costValue,
	)
}

// budgetDecision is the result of the guard's single read. It never inserts
// anything itself: the user row must exist before an override receipt can point
// at it.
type budgetDecision struct {
	Allowed  bool
	Refusal  string
	Override bool
	Spent    float64
	Budget   float64
}

// budgetCheck is the whole guard. It runs once, before a new user turn is
// accepted; it never looks at a running turn, so it cannot kill one. An empty
// budget is off. `lanjut` is the explicit one-turn override.
func (s *Server) budgetCheck(
	ctx context.Context,
	room store.Room,
	text string,
	hasAttachments bool,
) (budgetDecision, error) {
	if s.dailyBudget <= 0 {
		return budgetDecision{Allowed: true}, nil
	}
	start := jakartaDay(s.now())
	end := start.AddDate(0, 0, 1)
	spend, err := s.store.UsageSpendBetween(ctx, store.UsageSpendBetweenParams{
		FromAt: start,
		ToAt:   end,
	})
	if err != nil {
		// The guard must not make Iris unusable when the meter is unreadable.
		// Refuse nothing, but say so in the log; the turn is still metered.
		s.logger.Error("cannot read today's spend; budget guard skipped", "error", err)
		return budgetDecision{Allowed: true}, nil
	}
	decision := budgetDecision{Allowed: true, Spent: spend.CostUsd, Budget: s.dailyBudget}
	if spend.CostUsd < s.dailyBudget {
		return decision, nil
	}

	override := !hasAttachments && strings.EqualFold(strings.TrimSpace(text), "lanjut")
	if override {
		decision.Override = true
		s.logger.Warn(
			"budget override used",
			"room_id", room.ID.String(),
			"spent_usd", spend.CostUsd,
			"budget_usd", s.dailyBudget,
			"unknown_cost_turns", spend.UnknownCostTurns,
		)
		return decision, nil
	}

	decision.Allowed = false
	decision.Refusal = budgetRefusal(spend.CostUsd, s.dailyBudget)
	s.noteSystem(ctx, room.ID, decision.Refusal)
	s.logger.Warn(
		"budget refused a message",
		"room_id", room.ID.String(),
		"spent_usd", spend.CostUsd,
		"budget_usd", s.dailyBudget,
		"unknown_cost_turns", spend.UnknownCostTurns,
	)
	return decision, nil
}

// recordBudgetOverride writes the receipt for the one turn `lanjut` bought.
func (s *Server) recordBudgetOverride(
	ctx context.Context,
	roomID uuid.UUID,
	messageID *uuid.UUID,
	spent, budget float64,
) error {
	_, err := s.store.InsertBudgetOverride(ctx, store.InsertBudgetOverrideParams{
		ID:        uuid.New(),
		RoomID:    roomID,
		Day:       dateValue(jakartaDay(s.now())),
		SpentUsd:  spent,
		BudgetUsd: budget,
		MessageID: messageID,
		CreatedAt: s.now(),
	})
	return err
}

func budgetRefusal(spent, budget float64) string {
	return "Pesan ditolak: biaya hari ini " + pricing.FormatUSD(spent) +
		" sudah mencapai batas " + pricing.FormatUSD(budget) +
		". Kirim `lanjut` untuk satu giliran tambahan."
}

// ---- GET /api/usage -------------------------------------------------------

type usageTotalsView struct {
	Turns                int64   `json:"turns"`
	InputTokens          int64   `json:"input_tokens"`
	OutputTokens         int64   `json:"output_tokens"`
	TotalTokens          int64   `json:"total_tokens"`
	CacheReadTokens      int64   `json:"cache_read_tokens"`
	CacheWriteTokens     int64   `json:"cache_write_tokens"`
	CostUSD              float64 `json:"cost_usd"`
	UnknownCostTurns     int64   `json:"unknown_cost_turns"`
	UnreportedTurns      int64   `json:"unreported_turns"`
	UnreportedCacheTurns int64   `json:"unreported_cache_turns"`
}

func viewTotals(row store.UsageTotalsRow) usageTotalsView {
	return usageTotalsView{
		Turns:                row.Turns,
		InputTokens:          row.InputTokens,
		OutputTokens:         row.OutputTokens,
		TotalTokens:          row.TotalTokens,
		CacheReadTokens:      row.CacheReadTokens,
		CacheWriteTokens:     row.CacheWriteTokens,
		CostUSD:              row.CostUsd,
		UnknownCostTurns:     row.UnknownCostTurns,
		UnreportedTurns:      row.UnreportedTurns,
		UnreportedCacheTurns: row.UnreportedCacheTurns,
	}
}

type usageDayView struct {
	Day              string  `json:"day"`
	Turns            int64   `json:"turns"`
	Tokens           int64   `json:"tokens"`
	CostUSD          float64 `json:"cost_usd"`
	UnknownCostTurns int64   `json:"unknown_cost_turns"`
}

type usageRoomView struct {
	RoomID           string  `json:"room_id"`
	RoomName         string  `json:"room_name"`
	Turns            int64   `json:"turns"`
	Tokens           int64   `json:"tokens"`
	CostUSD          float64 `json:"cost_usd"`
	UnknownCostTurns int64   `json:"unknown_cost_turns"`
}

func (s *Server) getUsage(c echo.Context) error {
	now := s.now()
	from, to, err := usageWindow(c, now)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody(err.Error()))
	}

	var roomID *uuid.UUID
	if raw := strings.TrimSpace(c.QueryParam("room")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errorBody("Room itu tidak valid."))
		}
		if _, err := s.store.GetRoom(c.Request().Context(), parsed); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return c.JSON(http.StatusNotFound, errorBody("There is no room with that id."))
			}
			s.logger.Error("cannot load room for usage", "error", err)
			return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room ini."))
		}
		roomID = &parsed
	}

	ctx := c.Request().Context()
	totals, err := s.store.UsageTotals(ctx, store.UsageTotalsParams{
		RoomID: roomID,
		FromAt: from,
		ToAt:   to,
	})
	if err != nil {
		s.logger.Error("cannot read usage totals", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membaca catatan biaya."))
	}
	daily, err := s.store.UsageDaily(ctx, store.UsageDailyParams{
		RoomID: roomID,
		FromAt: from,
		ToAt:   to,
	})
	if err != nil {
		s.logger.Error("cannot read usage days", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membaca catatan biaya."))
	}
	rooms, err := s.store.UsageByRoom(ctx, store.UsageByRoomParams{
		FromAt: from,
		ToAt:   to,
	})
	if err != nil {
		s.logger.Error("cannot read usage by room", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membaca catatan biaya."))
	}
	todayStart := jakartaDay(now)
	today, err := s.store.UsageTotals(ctx, store.UsageTotalsParams{
		RoomID: roomID,
		FromAt: todayStart,
		ToAt:   now.Add(time.Nanosecond),
	})
	if err != nil {
		s.logger.Error("cannot read today's usage", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membaca catatan biaya."))
	}

	days := make([]usageDayView, 0, len(daily))
	for _, row := range daily {
		days = append(days, usageDayView{
			Day:              row.Day,
			Turns:            row.Turns,
			Tokens:           row.Tokens,
			CostUSD:          row.CostUsd,
			UnknownCostTurns: row.UnknownCostTurns,
		})
	}
	roomRows := make([]usageRoomView, 0, len(rooms))
	for _, row := range rooms {
		roomRows = append(roomRows, usageRoomView{
			RoomID:           row.RoomID.String(),
			RoomName:         row.RoomName,
			Turns:            row.Turns,
			Tokens:           row.Tokens,
			CostUSD:          row.CostUsd,
			UnknownCostTurns: row.UnknownCostTurns,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"from":   from.Format(time.RFC3339),
		"to":     to.Format(time.RFC3339),
		"totals": viewTotals(totals),
		"today":  viewTotals(today),
		"days":   days,
		"rooms":  roomRows,
		"budget": s.budgetView(spendToday(ctx, s, todayStart)),
		"prices": s.pricesView(),
	})
}

// spendToday is best-effort: the usage response itself is still useful when the
// spend row cannot be read, and the guard logs its own failure separately.
func spendToday(ctx context.Context, s *Server, start time.Time) *store.UsageSpendBetweenRow {
	row, err := s.store.UsageSpendBetween(ctx, store.UsageSpendBetweenParams{
		FromAt: start,
		ToAt:   start.AddDate(0, 0, 1),
	})
	if err != nil {
		s.logger.Error("cannot read today's spend for the usage panel", "error", err)
		return nil
	}
	return &row
}

func (s *Server) budgetView(spend *store.UsageSpendBetweenRow) map[string]any {
	if s.dailyBudget <= 0 {
		return map[string]any{"enabled": false}
	}
	view := map[string]any{"enabled": true, "usd": s.dailyBudget, "spent_usd": 0.0}
	if spend != nil {
		view["spent_usd"] = spend.CostUsd
		view["unknown_cost_turns"] = spend.UnknownCostTurns
	}
	return view
}

func (s *Server) pricesView() map[string]any {
	count, err := s.prices.Entries()
	view := map[string]any{"file": s.prices.Path(), "entries": count}
	if err != nil {
		view["error"] = err.Error()
	}
	return view
}

// usageWindow parses from/to. A bare date is a whole Jakarta day; an RFC3339
// value is exact. The default window is the last 14 Jakarta days, which is the
// chart's window.
func usageWindow(c echo.Context, now time.Time) (time.Time, time.Time, error) {
	to := now
	from := jakartaDay(now).AddDate(0, 0, -13)

	if raw := strings.TrimSpace(c.QueryParam("from")); raw != "" {
		parsed, isDate, err := parseUsageTime(raw)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("Tanggal awal tidak valid.")
		}
		from = parsed
		if isDate {
			from = jakartaDay(parsed)
		}
	}
	if raw := strings.TrimSpace(c.QueryParam("to")); raw != "" {
		parsed, isDate, err := parseUsageTime(raw)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("Tanggal akhir tidak valid.")
		}
		if isDate {
			to = jakartaDay(parsed).AddDate(0, 0, 1)
		} else {
			to = parsed
		}
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("Rentang tanggal itu tidak masuk akal.")
	}
	return from, to, nil
}

func parseUsageTime(raw string) (time.Time, bool, error) {
	if len(raw) == 10 {
		parsed, err := time.ParseInLocation("2006-01-02", raw, reminder.Jakarta())
		return parsed, true, err
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	return parsed, false, err
}

func dateValue(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}
