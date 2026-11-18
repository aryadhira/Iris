package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aryadhira/iris/internal/auth"
	"github.com/aryadhira/iris/internal/continuation"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/store"
	"github.com/aryadhira/iris/internal/turntrace"
)

const p12Reasoning = "PRIVATE-REASONING-MARKER: I should check the file before answering."

func p12Events() []hermes.Event {
	return []hermes.Event{
		{Kind: "started", RunID: "run_p12", TS: 100},
		{Kind: "tool", Name: "terminal", Status: "started", Preview: "printf 'p12' + 1 command", RunID: "run_p12", TS: 100.1},
		{Kind: "tool", Name: "terminal", Status: "completed", RunID: "run_p12", TS: 101.4},
		{Kind: "thinking", Name: "_thinking", Text: "Saya memeriksa hasil perintahnya dulu.", RunID: "run_p12", TS: 101.5},
		{Kind: "tool", Name: "write_file", Status: "started", Preview: "outbox/hasil.txt", RunID: "run_p12", TS: 102},
		{Kind: "tool", Name: "write_file", Status: "completed", RunID: "run_p12", TS: 103.6},
		{Kind: "delta", Text: "Beres, Boss.", RunID: "run_p12", TS: 103.7},
		{Kind: "done", Text: "Beres, Boss.\n\nSTATUS: DONE — selesai", Reasoning: p12Reasoning, RunID: "run_p12", TS: 104},
	}
}

func TestTurnPersistsStepsInOrderWithDurations(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_p12_steps", events: p12Events()}
	voice := &fakeVoice{}
	logger := testLogger()
	e := New(Options{
		Store:   st,
		Hermes:  hm,
		Voice:   voice,
		Auth:    auth.New(st, testPassphrase, false, logger),
		Logger:  logger,
		Version: "test",
		DataDir: t.TempDir(),
		WebDir:  t.TempDir(),
	})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages",
		map[string]string{"text": "kerjakan"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("send status = %d, body = %s", rec.Code, rec.Body.String())
	}
	frames := parseSSE(t, rec.Body.String())

	var steps []turntrace.Step
	doneIndex := -1
	for i, frame := range frames {
		switch frame.Name {
		case "step":
			var step turntrace.Step
			if err := json.Unmarshal([]byte(frame.Data), &step); err != nil {
				t.Fatalf("decode step: %v (%s)", err, frame.Data)
			}
			steps = append(steps, step)
		case "done":
			doneIndex = i
		}
	}
	if doneIndex < 0 {
		t.Fatal("the turn never sent done")
	}
	// Every first publication of a step must arrive before done. Later updates
	// to the same seq (corrected duration) are also before done by design.
	firstSeen := make(map[int]int)
	for i, frame := range frames {
		if frame.Name != "step" {
			continue
		}
		var step turntrace.Step
		if err := json.Unmarshal([]byte(frame.Data), &step); err != nil {
			t.Fatalf("decode step: %v", err)
		}
		if _, ok := firstSeen[step.Seq]; !ok {
			firstSeen[step.Seq] = i
		}
	}
	if len(firstSeen) < 3 {
		t.Fatalf("steps = %d, want at least 3", len(firstSeen))
	}
	for seq, index := range firstSeen {
		if index > doneIndex {
			t.Fatalf("step %d arrived after done", seq)
		}
	}

	var saved store.Message
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "assistant" {
			saved = row
		}
	}
	if saved.State != "done" {
		t.Fatalf("saved state = %q", saved.State)
	}
	if strings.Contains(saved.Body, "PRIVATE-REASONING-MARKER") || strings.Contains(saved.Body, "Saya memeriksa") {
		t.Fatalf("reasoning leaked into the body: %q", saved.Body)
	}
	var meta continuation.Meta
	if err := json.Unmarshal(saved.Meta, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if len(meta.Steps) < 3 {
		t.Fatalf("persisted steps = %d, want >= 3 (%+v)", len(meta.Steps), meta.Steps)
	}
	for i, step := range meta.Steps {
		if step.Seq != i+1 {
			t.Fatalf("persisted step %d seq = %d", i, step.Seq)
		}
		if step.DurationMs <= 0 || step.EndedAt == nil {
			t.Fatalf("persisted step %d has no duration: %+v", i, step)
		}
	}
	if !strings.Contains(meta.Reasoning, "PRIVATE-REASONING-MARKER") {
		t.Fatalf("reasoning excerpt was not stored: %q", meta.Reasoning)
	}
	if len(meta.Reasoning) > turntrace.MaxReasoning {
		t.Fatalf("reasoning excerpt = %d bytes", len(meta.Reasoning))
	}

	// Export is a user-facing surface: the private working text must not be in
	// it, and neither may the thinking step's text.
	export := request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/export.md", nil, cookie)
	if export.Code != http.StatusOK {
		t.Fatalf("export status = %d", export.Code)
	}
	if strings.Contains(export.Body.String(), "PRIVATE-REASONING-MARKER") ||
		strings.Contains(export.Body.String(), "Saya memeriksa") {
		t.Fatal("export contains reasoning text")
	}

	// Search reads the body only. A reasoning-only phrase cannot be a hit.
	search := request(t, e, http.MethodGet, "/api/search?q=PRIVATE-REASONING-MARKER", nil, cookie)
	if search.Code != http.StatusOK {
		t.Fatalf("search status = %d", search.Code)
	}
	if strings.Contains(search.Body.String(), "PRIVATE-REASONING-MARKER") {
		t.Fatal("search returned reasoning text")
	}

	// Speech receives the visible body, which never carried reasoning.
	speak := request(t, e, http.MethodPost, "/api/voice/speak", map[string]string{"text": saved.Body}, cookie)
	if speak.Code != http.StatusOK {
		t.Fatalf("speak status = %d, body = %s", speak.Code, speak.Body.String())
	}
	if strings.Contains(voice.speakText, "PRIVATE-REASONING-MARKER") ||
		strings.Contains(voice.speakText, "Saya memeriksa") {
		t.Fatalf("TTS received reasoning text: %q", voice.speakText)
	}
}

// readNextFrame scans one SSE frame from a live response.
func readNextFrame(t *testing.T, scanner *bufio.Scanner) (string, string, bool) {
	t.Helper()
	name := ""
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if name != "" || len(data) > 0 {
				return name, strings.Join(data, "\n"), true
			}
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return "", "", false
}

func TestSnapshotCarriesInFlightStepsAndLateSubscriberSeesTheSameList(t *testing.T) {
	st := newFakeStore()
	ready := make(chan struct{})
	release := make(chan struct{})
	hm := &fakeHermes{
		createID: "api_p12_snapshot",
		onStream: func(_ context.Context, emit func(hermes.Event) error) error {
			if err := emit(hermes.Event{Kind: "started", RunID: "run_p12_live", TS: 100}); err != nil {
				return err
			}
			if err := emit(hermes.Event{Kind: "tool", Name: "terminal", Status: "started", Preview: "sleep 3", RunID: "run_p12_live", TS: 100.1}); err != nil {
				return err
			}
			if err := emit(hermes.Event{Kind: "tool", Name: "write_file", Status: "started", Preview: "outbox/x.txt", RunID: "run_p12_live", TS: 100.5}); err != nil {
				return err
			}
			close(ready)
			<-release
			if err := emit(hermes.Event{Kind: "done", Text: "selesai", RunID: "run_p12_live", TS: 105}); err != nil {
				return err
			}
			return nil
		},
	}
	logger := testLogger()
	e := New(Options{
		Store:            st,
		Hermes:           hm,
		Auth:             auth.New(st, testPassphrase, false, logger),
		Logger:           logger,
		Version:          "test",
		DataDir:          t.TempDir(),
		WebDir:           t.TempDir(),
		ActivityInterval: 20 * time.Millisecond,
	})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)
	server := httptest.NewServer(e)
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		req := httptest.NewRequest(http.MethodPost,
			"/api/rooms/"+room.ID.String()+"/messages",
			strings.NewReader(`{"text":"mulai"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		e.ServeHTTP(httptest.NewRecorder(), req)
	}()

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("turn never reached the in-flight steps")
	}

	subscribe := func() (*http.Response, roomSnapshot) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			server.URL+"/api/rooms/"+room.ID.String()+"/events", nil)
		if err != nil {
			t.Fatalf("events request: %v", err)
		}
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("events request: %v", err)
		}
		return resp, readSnapshot(t, resp)
	}

	respA, snapA := subscribe()
	defer respA.Body.Close()
	respB, snapB := subscribe()
	defer respB.Body.Close()

	if len(snapA.Steps) != 2 || len(snapB.Steps) != 2 {
		t.Fatalf("in-flight steps = %d / %d, want 2 / 2", len(snapA.Steps), len(snapB.Steps))
	}
	for i := range snapA.Steps {
		if snapA.Steps[i].Seq != snapB.Steps[i].Seq ||
			snapA.Steps[i].Kind != snapB.Steps[i].Kind ||
			snapA.Steps[i].Name != snapB.Steps[i].Name {
			t.Fatalf("late subscribers disagree: %+v vs %+v", snapA.Steps, snapB.Steps)
		}
	}
	if snapA.Steps[0].Status != turntrace.StatusRunning {
		t.Fatalf("in-flight step status = %q, want running", snapA.Steps[0].Status)
	}

	// The activity heartbeat carries the age of the last real event; it is a
	// frame on the same stream, not a hidden timer in the browser.
	scanner := bufio.NewScanner(respA.Body)
	deadline := time.Now().Add(2 * time.Second)
	sawActivity := false
	for time.Now().Before(deadline) {
		name, data, ok := readNextFrame(t, scanner)
		if !ok {
			break
		}
		if name != "activity" {
			continue
		}
		var payload struct {
			AgeMs *int64 `json:"ageMs"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("decode activity: %v", err)
		}
		if payload.AgeMs == nil || *payload.AgeMs < 0 {
			t.Fatalf("activity age = %v", payload.AgeMs)
		}
		sawActivity = true
		break
	}
	if !sawActivity {
		t.Fatal("no activity frame arrived while the turn was silent")
	}

	close(release)
	select {
	case <-turnDone:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish")
	}
	var assistant store.Message
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "assistant" {
			assistant = row
		}
	}
	if assistant.State == "" {
		t.Fatal("the finished turn left no assistant row")
	}
	if assistant.State != "done" {
		t.Fatalf("assistant state = %q, want done", assistant.State)
	}
}
