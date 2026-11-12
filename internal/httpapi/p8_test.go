package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/auth"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/pricing"
	"github.com/aryadhira/iris/internal/store"
)

func floatPtr(v float64) *float64 { return &v }

func usagePriceFile(t *testing.T) *pricing.Table {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prices.json")
	body := `{
	  "deepseek-v4.1-flash": {"input":0.15,"output":0.60,"cache_read":0.003,"cache_write":null,"unit":"USD_per_1M","source":"test"},
	  "other-model": {"input":1.0,"output":2.0,"unit":"USD_per_1M","source":"test"}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write prices: %v", err)
	}
	return pricing.New(path)
}

func newTestAppWith(t *testing.T, st *fakeStore, hm hermes.Client, opts Options) *echo.Echo {
	t.Helper()
	logger := testLogger()
	if opts.Store == nil {
		opts.Store = st
	}
	if opts.Hermes == nil {
		opts.Hermes = hm
	}
	opts.Auth = auth.New(st, testPassphrase, false, logger)
	opts.Logger = logger
	opts.Version = "test"
	if opts.DataDir == "" {
		opts.DataDir = t.TempDir()
	}
	opts.WebDir = t.TempDir()
	return New(opts)
}

func usageTurn(runID string, usage *hermes.Usage, runtime hermes.Runtime) []hermes.Event {
	return []hermes.Event{
		{Kind: "started", RunID: runID},
		{Kind: "delta", Text: "Halo, Boss.", RunID: runID},
		{Kind: "done", Text: "Halo, Boss.", RunID: runID, Usage: usage, Runtime: runtime},
	}
}

func TestUsageCaptureStoresOneRowWithCost(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_usage"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t)})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	hm.events = usageTurn("run_use_1",
		&hermes.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000},
		hermes.Runtime{Provider: "opencode-go", Model: "deepseek-v4.1-flash"},
	)

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "halo"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(st.usageRows) != 1 {
		t.Fatalf("usage rows = %d, want 1", len(st.usageRows))
	}
	row := st.usageRows[0]
	if row.RunID != "run_use_1" || row.RoomID != room.ID {
		t.Fatalf("usage row = %+v", row)
	}
	if row.Provider != "opencode-go" || row.Model != "deepseek-v4.1-flash" {
		t.Fatalf("runtime stored = %q/%q", row.Provider, row.Model)
	}
	if row.InputTokens != 1_000_000 || row.OutputTokens != 1_000_000 || row.TotalTokens != 2_000_000 {
		t.Fatalf("counters = %+v", row)
	}
	if !row.UsageReported {
		t.Fatal("usage_reported = false for a payload that carried usage")
	}
	if row.CostUsd == nil {
		t.Fatal("cost is nil although the model has a price")
	}
	// $0.15 input + $0.60 output.
	if diff := *row.CostUsd - 0.75; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %v, want 0.75", *row.CostUsd)
	}
	if row.MessageID == nil {
		t.Fatal("usage row is not linked to the assistant message")
	}
}

func TestUsageAbsentPayloadIsFlaggedAndNotFree(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_usage_absent"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t)})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	hm.events = []hermes.Event{
		{Kind: "started", RunID: "run_absent"},
		{Kind: "delta", Text: "Halo", RunID: "run_absent"},
		{Kind: "done", Text: "Halo", RunID: "run_absent"},
	}
	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "halo"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(st.usageRows) != 1 {
		t.Fatalf("usage rows = %d, want 1 (an absent payload is still recorded)", len(st.usageRows))
	}
	row := st.usageRows[0]
	if row.UsageReported {
		t.Fatal("usage_reported = true for a payload without usage")
	}
	if row.CostUsd != nil {
		t.Fatalf("cost = %v, want nil for an unreported turn", *row.CostUsd)
	}
	if row.InputTokens != 0 || row.TotalTokens != 0 {
		t.Fatalf("absent counters = %+v, want zeros", row)
	}
}

func TestUsageUnknownModelCostIsNull(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_usage_unknown"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t)})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	hm.events = usageTurn("run_unknown_model",
		&hermes.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
		hermes.Runtime{Provider: "somewhere", Model: "a-model-with-no-price"},
	)
	request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "halo"}, cookie)
	if len(st.usageRows) != 1 {
		t.Fatalf("usage rows = %d", len(st.usageRows))
	}
	if st.usageRows[0].CostUsd != nil {
		t.Fatalf("cost = %v, want nil for an unpriced model", *st.usageRows[0].CostUsd)
	}
}

func TestUsageAPIRequiresAuthAndSumsTheRows(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_usage_api"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t)})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)
	now := time.Now()
	st.usageRows = []store.Usage{
		{ID: uuid.New(), RunID: "r1", RoomID: room.ID, Provider: "p", Model: "m", InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CostUsd: floatPtr(0.001), UsageReported: true, CreatedAt: now},
		{ID: uuid.New(), RunID: "r2", RoomID: room.ID, Provider: "p", Model: "m", InputTokens: 200, OutputTokens: 20, TotalTokens: 220, CostUsd: floatPtr(0.002), UsageReported: true, CreatedAt: now},
	}
	if rec := request(t, e, http.MethodGet, "/api/usage", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without cookie = %d, want 401", rec.Code)
	}
	rec := request(t, e, http.MethodGet, "/api/usage?room="+room.ID.String(), nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Totals struct {
			Turns       int64   `json:"turns"`
			TotalTokens int64   `json:"total_tokens"`
			CostUSD     float64 `json:"cost_usd"`
		} `json:"totals"`
		Days []struct {
			Day   string  `json:"day"`
			Turns int64   `json:"turns"`
			Cost  float64 `json:"cost_usd"`
		} `json:"days"`
		Rooms []struct {
			RoomID string `json:"room_id"`
			Turns  int64  `json:"turns"`
		} `json:"rooms"`
		Today struct {
			Turns int64 `json:"turns"`
		} `json:"today"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Totals.Turns != 2 || body.Totals.TotalTokens != 330 {
		t.Fatalf("totals = %+v", body.Totals)
	}
	if diff := body.Totals.CostUSD - 0.003; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want 0.003", body.Totals.CostUSD)
	}
	if len(body.Days) != 1 || body.Days[0].Turns != 2 {
		t.Fatalf("days = %+v", body.Days)
	}
	if len(body.Rooms) != 1 || body.Rooms[0].Turns != 2 {
		t.Fatalf("rooms = %+v", body.Rooms)
	}
	if body.Today.Turns != 2 {
		t.Fatalf("today = %+v", body.Today)
	}
}

func TestBudgetBlocksAtThresholdAndAllowsExactlyOneOverride(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_budget"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t), DailyBudgetUSD: 0.0001})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	st.usageRows = append(st.usageRows, store.Usage{
		ID: uuid.New(), RunID: "spent", RoomID: room.ID,
		CostUsd: floatPtr(0.0025), UsageReported: true, CreatedAt: time.Now(),
	})
	hm.events = usageTurn("run_budget_1",
		&hermes.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		hermes.Runtime{Provider: "p", Model: "other-model"},
	)

	before := len(st.messagesFor(room.ID))
	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "halo"}, cookie)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked message status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Pesan ditolak") || !strings.Contains(rec.Body.String(), "lanjut") {
		t.Fatalf("refusal copy is not the Indonesian guard: %s", rec.Body.String())
	}
	if got := len(st.messagesFor(room.ID)); got != before+1 {
		t.Fatalf("messages after refusal = %d, want %d (one visible system notice, no user row)", got, before+1)
	}
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "user" {
			t.Fatal("a refused message left a user row")
		}
	}
	if len(st.overrides) != 0 {
		t.Fatalf("overrides = %d, want 0 before lanjut", len(st.overrides))
	}

	rec = request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "lanjut"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("lanjut status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if len(st.overrides) != 1 {
		t.Fatalf("overrides = %d, want 1 after lanjut", len(st.overrides))
	}
	if st.overrides[0].SpentUsd != 0.0025 || st.overrides[0].BudgetUsd != 0.0001 {
		t.Fatalf("override receipt = %+v", st.overrides[0])
	}
	users := 0
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "user" && row.Body == "lanjut" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("lanjut user rows = %d, want 1", users)
	}

	// The pass is one turn, not a new budget: the next message is blocked again.
	rec = request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "halo lagi"}, cookie)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("message after the override = %d, want 429", rec.Code)
	}
	if len(st.overrides) != 1 {
		t.Fatalf("overrides = %d after a second refusal, want still 1", len(st.overrides))
	}
}

func TestBudgetGuardNeverLooksAtARunningTurn(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_budget_running"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t), DailyBudgetUSD: 0.0001})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	// No spend when the turn starts.
	hm.onStream = func(_ context.Context, emit func(hermes.Event) error) error {
		if err := emit(hermes.Event{Kind: "delta", Text: "mulai", RunID: "run_running"}); err != nil {
			return err
		}
		// The turn itself pushes spend over the budget while it is running. The
		// guard must not exist mid-turn.
		st.mu.Lock()
		st.usageRows = append(st.usageRows, store.Usage{
			ID: uuid.New(), RunID: "mid", RoomID: room.ID,
			CostUsd: floatPtr(9.99), UsageReported: true, CreatedAt: time.Now(),
		})
		st.mu.Unlock()
		return emit(hermes.Event{
			Kind: "done", Text: "mulai selesai", RunID: "run_running",
			Usage:   &hermes.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			Runtime: hermes.Runtime{Provider: "p", Model: "other-model"},
		})
	}
	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "kerja panjang"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	found := false
	refused := false
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "assistant" && strings.Contains(row.Body, "mulai") {
			found = true
		}
		if row.Role == "system" && strings.Contains(row.Body, "Pesan ditolak") {
			refused = true
		}
	}
	if !found {
		t.Fatal("the running turn was killed by the budget guard")
	}
	if refused {
		t.Fatal("the guard wrote a refusal while a turn was already running")
	}
}

func TestRetryReusesTheUserMessageAndSupersedesTheOldReply(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_retry"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t)})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	now := time.Now()
	user := store.Message{ID: uuid.New(), RoomID: room.ID, Role: "user", Body: "halo", Meta: []byte("{}"), CreatedAt: now.Add(-3 * time.Second)}
	old := store.Message{ID: uuid.New(), RoomID: room.ID, Role: "assistant", Body: "balasan lama", Meta: []byte("{}"), CreatedAt: now.Add(-2 * time.Second)}
	st.messages = append(st.messages, user, old)

	hm.events = usageTurn("run_retry_1",
		&hermes.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		hermes.Runtime{Provider: "p", Model: "other-model"},
	)
	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages/"+user.ID.String()+"/retry", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("retry did not stream a turn: %s", rec.Body.String())
	}

	userRows, assistantRows := 0, 0
	var oldAfter store.Message
	for _, row := range st.messagesFor(room.ID) {
		switch row.Role {
		case "user":
			userRows++
		case "assistant":
			assistantRows++
			if row.ID == old.ID {
				oldAfter = row
			}
		}
	}
	if userRows != 1 {
		t.Fatalf("user rows = %d, want 1 (a retry is not a resend)", userRows)
	}
	if assistantRows != 2 {
		t.Fatalf("assistant rows = %d, want 2 (old kept, new added)", assistantRows)
	}
	var meta map[string]any
	if err := json.Unmarshal(oldAfter.Meta, &meta); err != nil {
		t.Fatalf("decode old meta: %v", err)
	}
	if meta["superseded"] != true {
		t.Fatalf("old reply meta = %s, want superseded:true", oldAfter.Meta)
	}
}

func TestRetryRefusesWhileTheRoomIsBusy(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_retry_busy"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t)})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)
	user := store.Message{ID: uuid.New(), RoomID: room.ID, Role: "user", Body: "halo", Meta: []byte("{}"), CreatedAt: time.Now().Add(-time.Second)}
	st.messages = append(st.messages, user)
	st.runs["run_busy"] = store.Run{RunID: "run_busy", RoomID: room.ID, Status: "running"}

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages/"+user.ID.String()+"/retry", nil, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "masih bekerja") {
		t.Fatalf("409 copy is not Indonesian: %s", rec.Body.String())
	}
	if len(hm.events) != 0 && hm.streamCalls > 0 {
		t.Fatal("a busy retry still reached Hermes")
	}
}

func TestRetryRejectsAnAssistantMessageId(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_retry_bad"}
	e := newTestAppWith(t, st, hm, Options{Prices: usagePriceFile(t)})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)
	assistant := store.Message{ID: uuid.New(), RoomID: room.ID, Role: "assistant", Body: "x", Meta: []byte("{}"), CreatedAt: time.Now()}
	st.messages = append(st.messages, assistant)

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages/"+assistant.ID.String()+"/retry", nil, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// flushRecorder is an http.ResponseWriter that counts Flush calls and records
// when the first flush happened. httptest.ResponseRecorder only keeps a bool,
// which cannot tell one flush from three.
type flushRecorder struct {
	mu         sync.Mutex
	header     http.Header
	body       bytes.Buffer
	code       int
	flushes    int
	firstFlush time.Time
	lastWrite  time.Time
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: http.Header{}}
}

func (w *flushRecorder) Header() http.Header { return w.header }

func (w *flushRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastWrite = time.Now()
	return w.body.Write(p)
}

func (w *flushRecorder) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.code == 0 {
		w.code = code
	}
}

func (w *flushRecorder) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushes++
	if w.firstFlush.IsZero() {
		w.firstFlush = time.Now()
	}
}

func TestVoiceSpeakStreamProxyFlushesEachSentence(t *testing.T) {
	reader, writer := io.Pipe()
	fv := &fakeVoice{
		speakStream: func(_ context.Context, text string) (io.ReadCloser, error) {
			if !strings.Contains(text, "Selesai") {
				t.Errorf("proxy changed the text: %q", text)
			}
			go func() {
				for i, sentence := range []string{"Selesai, port kosong.", "Detailnya ada di layar."} {
					fmt.Fprintf(writer, `{"type":"audio","index":%d,"text":%q,"audio":"AAAA"}`+"\n", i, sentence)
					time.Sleep(80 * time.Millisecond)
				}
				fmt.Fprint(writer, `{"type":"done","chunks":2}`+"\n")
				writer.Close()
			}()
			return reader, nil
		},
	}
	st := newFakeStore()
	logger := testLogger()
	e := New(Options{
		Store: st, Hermes: &fakeHermes{}, Voice: fv,
		Auth: auth.New(st, testPassphrase, false, logger), Logger: logger,
		Version: "test", DataDir: t.TempDir(),
	})
	cookie := login(t, e)

	req := httptest.NewRequest(http.MethodPost, "/api/voice/speak/stream",
		strings.NewReader(`{"text":"**Selesai**, port kosong. Detailnya ada di layar."}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.AddCookie(cookie)
	rec := newFlushRecorder()

	done := make(chan struct{})
	go func() {
		e.ServeHTTP(rec, req)
		close(done)
	}()
	<-done

	rec.mu.Lock()
	defer rec.mu.Unlock()
	body := rec.body.String()
	if rec.code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.code, body)
	}
	if got := rec.header.Get(echo.HeaderContentType); got != "application/x-ndjson" {
		t.Fatalf("content-type = %q", got)
	}
	if strings.Count(body, "\n") < 3 {
		t.Fatalf("body has %d lines, want 3: %s", strings.Count(body, "\n"), body)
	}
	if rec.flushes < 3 {
		t.Fatalf("flushes = %d, want at least 3 (a buffered proxy would flush once)", rec.flushes)
	}
	if rec.firstFlush.IsZero() || !rec.firstFlush.Before(rec.lastWrite) {
		t.Fatalf("first flush at %v, last write at %v: the first line was not sent early", rec.firstFlush, rec.lastWrite)
	}
}
