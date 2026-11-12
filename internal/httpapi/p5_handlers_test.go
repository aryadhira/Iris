package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/auth"
	"github.com/aryadhira/iris/internal/store"
)

func newTestAppWithWebDir(t *testing.T, st *fakeStore, webDir string) *echo.Echo {
	t.Helper()
	logger := testLogger()
	return New(Options{
		Store:   st,
		Hermes:  &fakeHermes{},
		Auth:    auth.New(st, testPassphrase, false, logger),
		Logger:  logger,
		Version: "test",
		DataDir: t.TempDir(),
		WebDir:  webDir,
	})
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestSPAHistoryFallbackAndAssets(t *testing.T) {
	webDir := t.TempDir()
	writeFile(t, filepath.Join(webDir, "index.html"), "<!doctype html><html><body>iris shell</body></html>")
	writeFile(t, filepath.Join(webDir, "assets", "index-abc123.js"), "console.log('iris')")
	writeFile(t, filepath.Join(webDir, "favicon.ico"), "icon")

	st := newFakeStore()
	e := newTestAppWithWebDir(t, st, webDir)

	// A client-side route must return the shell, with no-cache so a deploy is
	// visible on the next reload.
	rec := request(t, e, http.MethodGet, "/r/00000000-0000-0000-0000-000000000000", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /r/x = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "iris shell") {
		t.Fatalf("history fallback body = %q, want index.html", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
		t.Fatalf("index Cache-Control = %q", got)
	}

	// Hashed build output is immutable for a year.
	rec = request(t, e, http.MethodGet, "/assets/index-abc123.js", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Fatalf("asset body = %q", rec.Body.String())
	}

	// A missing hashed asset must not become an HTML page.
	rec = request(t, e, http.MethodGet, "/assets/missing-xyz.js", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "iris shell") {
		t.Fatal("missing asset returned index.html")
	}

	// Root and plain files are public too (the login screen has no cookie yet).
	rec = request(t, e, http.MethodGet, "/", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "iris shell") {
		t.Fatalf("GET / = %d %q", rec.Code, rec.Body.String())
	}

	// /api/* never returns HTML: it is JSON 404, even without a cookie the
	// middleware answers first with JSON 401.
	rec = request(t, e, http.MethodGet, "/api/nope", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/nope = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("/api/nope Content-Type = %q, want JSON", ct)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
		t.Fatalf("/api/nope returned HTML: %s", rec.Body.String())
	}

	cookie := login(t, e)
	rec = request(t, e, http.MethodGet, "/api/nope", nil, cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("authenticated /api/nope = %d, want 404", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("API 404 is not JSON: %v (%s)", err, rec.Body.String())
	}
	if body["error"] == "" {
		t.Fatal("API 404 has no error message")
	}
}

func TestSPAMissingBuildKeepsAPIAvailable(t *testing.T) {
	st := newFakeStore()
	e := newTestAppWithWebDir(t, st, filepath.Join(t.TempDir(), "not-built"))

	rec := request(t, e, http.MethodGet, "/", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET / without build = %d, want 404", rec.Code)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
		t.Fatalf("missing build returned HTML: %q", rec.Body.String())
	}

	rec = request(t, e, http.MethodGet, "/api/health", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("health with missing build = %d, want 200", rec.Code)
	}
}

func TestSearchFindsKnownPhraseAndIgnoresUnseen(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	e := newTestAppWithWebDir(t, st, t.TempDir())
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	known, err := st.InsertMessage(context.Background(), store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Role:   "assistant",
		Body:   "Kapal itu berlayar ke pelabuhan tanjung priok membawa kopi.",
		Meta:   []byte("{}"),
	})
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	rec := request(t, e, http.MethodGet, "/api/search?q=pelabuhan", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Results []searchResultView `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(body.Results) != 1 {
		t.Fatalf("results = %d, want 1 (%s)", len(body.Results), rec.Body.String())
	}
	got := body.Results[0]
	if got.MessageID != known.ID.String() || got.RoomID != room.ID.String() || got.RoomName != "Work" {
		t.Fatalf("result = %+v, want room %s message %s", got, room.ID, known.ID)
	}
	if !strings.Contains(got.Snippet, "<mark>") {
		t.Fatalf("snippet is not highlighted: %q", got.Snippet)
	}

	rec = request(t, e, http.MethodGet, "/api/search?q=xylophonezz", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("unseen search = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode empty search: %v", err)
	}
	if len(body.Results) != 0 {
		t.Fatalf("unseen phrase returned %d results", len(body.Results))
	}

	// Auth is required, like every other /api route.
	rec = request(t, e, http.MethodGet, "/api/search?q=pelabuhan", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("search without cookie = %d, want 401", rec.Code)
	}
}

func TestExportContainsMessagesAndAttachmentNames(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	e := newTestAppWithWebDir(t, st, t.TempDir())
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	userMessage, err := st.InsertMessage(context.Background(), store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Role:   "user",
		Body:   "Tolong periksa ini.\n\n```go\nfmt.Println(\"halo\")\n```",
		Meta:   []byte("{}"),
	})
	if err != nil {
		t.Fatalf("insert user message: %v", err)
	}
	assistantMessage, err := st.InsertMessage(context.Background(), store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Role:   "assistant",
		Body:   "Sudah diperiksa. Hasilnya bersih.",
		Meta:   []byte("{}"),
	})
	if err != nil {
		t.Fatalf("insert assistant message: %v", err)
	}

	for _, f := range []store.InsertFileParams{
		{
			ID: uuid.New(), RoomID: room.ID, MessageID: &userMessage.ID, Source: "user",
			Name: "laporan-pelabuhan.pdf", Path: "inbox/x-laporan-pelabuhan.pdf",
			Mime: "application/pdf", Size: 2048, Sha256: "abc",
		},
		{
			ID: uuid.New(), RoomID: room.ID, MessageID: &assistantMessage.ID, Source: "agent",
			Name: "ringkasan.md", Path: "outbox/ringkasan.md",
			Mime: "text/markdown", Size: 512, Sha256: "def",
		},
	} {
		if _, err := st.InsertFile(context.Background(), f); err != nil {
			t.Fatalf("insert file: %v", err)
		}
	}

	rec := request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/export.md", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d, body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("export Content-Type = %q", ct)
	}
	disposition := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, ".md") {
		t.Fatalf("export Content-Disposition = %q", disposition)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"# Work", "**Topik:** ship", "**Tanggal:**",
		"**Kamu**", "**Hermes**",
		"Tolong periksa ini.", "```go\nfmt.Println(\"halo\")\n```",
		"Sudah diperiksa. Hasilnya bersih.",
		"laporan-pelabuhan.pdf", "ringkasan.md", "2,0 KB", "512 B",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("export is missing %q:\n%s", want, body)
		}
	}

	// The export is readable only with the owner cookie.
	rec = request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/export.md", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("export without cookie = %d, want 401", rec.Code)
	}
}

func TestIndonesianDateHelpers(t *testing.T) {
	when := time.Date(2026, 11, 12, 9, 5, 0, 0, time.UTC)
	if got := indonesianDate(when); got != "12 November 2026" {
		t.Fatalf("date = %q", got)
	}
	if got := indonesianDateTime(when); got != "12 November 2026, 09.05" {
		t.Fatalf("datetime = %q", got)
	}
}
