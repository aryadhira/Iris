package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/continuation"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/store"
)

func TestPersistedReplyIsCleanedButThePumpSeesTheStatus(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_p9_clean"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	raw := "Sudah jalan, proses proc_6a1c1303b005 berjalan -- tenang.\n\nSTATUS: RUNNING — 90 detik"
	hm.events = []hermes.Event{
		{Kind: "delta", Text: "Sudah jalan, proses proc_6a1c1303b005 berjalan --", RunID: "run_p9"},
		{Kind: "delta", Text: " tenang.\n\nSTATUS: RUNNING — 90 detik", RunID: "run_p9"},
		{Kind: "done", Text: raw, RunID: "run_p9"},
	}

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "mulai"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d, body %s", rec.Code, rec.Body.String())
	}

	st.mu.Lock()
	var assistant store.Message
	for _, row := range st.messages {
		if row.RoomID == room.ID && row.Role == "assistant" {
			assistant = row
		}
	}
	continuations := len(st.continuations)
	st.mu.Unlock()

	if assistant.Body != "Sudah jalan, proses proc_6a1c1303b005 berjalan — tenang." {
		t.Fatalf("stored body = %q", assistant.Body)
	}
	if strings.Contains(assistant.Body, "STATUS:") || strings.Contains(assistant.Body, " -- ") {
		t.Fatalf("stored body still leaks: %q", assistant.Body)
	}

	var meta continuation.Meta
	if err := json.Unmarshal(assistant.Meta, &meta); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if meta.StatusLine != "STATUS: RUNNING — 90 detik" {
		t.Fatalf("pump status line = %q", meta.StatusLine)
	}
	if len(meta.Procs) != 1 || meta.Procs[0] != "proc_6a1c1303b005" {
		t.Fatalf("pump procs = %v", meta.Procs)
	}
	if continuations != 1 {
		t.Fatalf("continuations = %d, want 1", continuations)
	}
}

func TestLiveDeltasHoldTheDashAndNeverShowStatus(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_p9_live"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	hm.events = []hermes.Event{
		{Kind: "delta", Text: "Halo --", RunID: "run_live"},
		{Kind: "delta", Text: " auto.\nSTATUS: DON", RunID: "run_live"},
		{Kind: "delta", Text: "E — selesai\nSelesai.\n", RunID: "run_live"},
		{Kind: "done", Text: "Halo -- auto.\nSTATUS: DONE — selesai\nSelesai.\n", RunID: "run_live"},
	}

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "halo"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d, body %s", rec.Code, rec.Body.String())
	}

	events := parseSSE(t, rec.Body.String())
	var deltas []string
	for _, ev := range events {
		if ev.Name != "delta" {
			continue
		}
		var payload struct{ Text string }
		if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
			t.Fatalf("delta payload: %v", err)
		}
		deltas = append(deltas, payload.Text)
	}
	if len(deltas) == 0 || deltas[0] != "Halo " {
		t.Fatalf("first delta = %q, want the trailing -- held back", deltas[0])
	}
	joined := strings.Join(deltas, "")
	if joined != "Halo — auto.\nSelesai.\n" {
		t.Fatalf("joined deltas = %q", joined)
	}
	for _, delta := range deltas {
		if strings.Contains(delta, "STATUS:") || strings.Contains(delta, "--") {
			t.Fatalf("a live delta leaked a contract token: %q", delta)
		}
	}
}

func TestExportAndSearchCleanOldRows(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	e := newTestAppWithWebDir(t, st, t.TempDir())
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	body := "Kapal berlayar -- jauh.\n\nSTATUS: DONE — selesai"
	if _, err := st.InsertMessage(context.Background(), store.InsertMessageParams{
		ID: uuid.New(), RoomID: room.ID, Role: "assistant", Body: body, Meta: []byte("{}"),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/export.md", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d, body %s", rec.Code, rec.Body.String())
	}
	exported := rec.Body.String()
	if strings.Contains(exported, "STATUS:") || strings.Contains(exported, " -- ") {
		t.Fatalf("export leaks a contract token:\n%s", exported)
	}
	if !strings.Contains(exported, "Kapal berlayar — jauh.") {
		t.Fatalf("export did not keep the prose:\n%s", exported)
	}

	rec = request(t, e, http.MethodGet, "/api/search?q=kapal", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, body %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Results []searchResultView `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results = %d, want 1 (%s)", len(result.Results), rec.Body.String())
	}
	snippet := result.Results[0].Snippet
	if strings.Contains(snippet, "STATUS:") || strings.Contains(snippet, " -- ") {
		t.Fatalf("snippet leaks a contract token: %q", snippet)
	}
	if !strings.Contains(snippet, "Kapal") {
		t.Fatalf("snippet lost the match: %q", snippet)
	}
}

func TestSpeakEndpointsCleanTheTextBeforeTheSidecar(t *testing.T) {
	fv := &fakeVoice{}
	e := newVoiceTestApp(t, fv)
	cookie := login(t, e)
	payload := `{"text":"Selesai -- cepat.\n\nSTATUS: DONE — selesai"}`

	rec := voicePost(t, e, "/api/voice/speak", strings.NewReader(payload), echoMIMEJSON, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("speak = %d, body %s", rec.Code, rec.Body.String())
	}
	if fv.speakText != "Selesai — cepat." {
		t.Fatalf("sidecar speak text = %q", fv.speakText)
	}
	if strings.Contains(fv.speakText, "STATUS:") || strings.Contains(fv.speakText, " -- ") {
		t.Fatalf("sidecar got a contract token: %q", fv.speakText)
	}

	rec = voicePost(t, e, "/api/voice/speak/stream", strings.NewReader(payload), echoMIMEJSON, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("stream = %d, body %s", rec.Code, rec.Body.String())
	}
	if fv.streamText != "Selesai — cepat." {
		t.Fatalf("sidecar stream text = %q", fv.streamText)
	}
}

const echoMIMEJSON = "application/json"
