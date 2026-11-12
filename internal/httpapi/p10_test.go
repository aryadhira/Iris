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

	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/store"
)

func TestRoomEventsSnapshotCarriesRunPartialAndApproval(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_p10_snapshot"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	started := time.Now().Add(-42 * time.Second)
	st.mu.Lock()
	st.runs["run_live"] = store.Run{
		RunID: "run_live", RoomID: room.ID, SessionID: room.HermesSessionID,
		Status: "running", StartedAt: started, UpdatedAt: started,
	}
	st.messages = append(st.messages, store.Message{
		ID: uuid.New(), RoomID: room.ID, Role: "assistant", Body: "sudah setengah jalan",
		CreatedAt: time.Now(), Meta: []byte("{}"), State: "streaming",
	})
	approvalID := uuid.New()
	st.approvals = append(st.approvals, store.Approval{
		ID: approvalID, RoomID: room.ID, RunID: "run_live", RequestID: "req_live",
		ToolName: "terminal", Command: "redacted", Choices: []string{"once", "deny"},
		State: "pending", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	st.mu.Unlock()

	server := httptest.NewServer(e)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/rooms/"+room.ID.String()+"/events", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer resp.Body.Close()

	snapshot := readSnapshot(t, resp)
	if snapshot.Run == nil {
		t.Fatal("snapshot has no run")
	}
	if snapshot.Run.ID != "run_live" || snapshot.Run.Status != "running" {
		t.Fatalf("snapshot run = %+v", snapshot.Run)
	}
	if snapshot.Run.Continuation != 0 {
		t.Fatalf("snapshot continuation = %d, want 0", snapshot.Run.Continuation)
	}
	if snapshot.Partial != "sudah setengah jalan" {
		t.Fatalf("snapshot partial = %q", snapshot.Partial)
	}
	if len(snapshot.Approvals) != 1 || snapshot.Approvals[0].ID != approvalID.String() {
		t.Fatalf("snapshot approvals = %+v", snapshot.Approvals)
	}
}

func readSnapshot(t *testing.T, resp *http.Response) roomSnapshot {
	t.Helper()
	scanner := bufio.NewScanner(resp.Body)
	event := ""
	data := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			if event == "snapshot" {
				var snapshot roomSnapshot
				if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
					t.Fatalf("decode snapshot: %v (%s)", err, data)
				}
				return snapshot
			}
			event, data = "", ""
		}
	}
	t.Fatalf("stream ended before a snapshot: %v", scanner.Err())
	return roomSnapshot{}
}

func TestStreamingRowIsPersistedWhileTheTurnRunsAndCleanedAtTheEnd(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_p10_partial"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	first := make(chan struct{})
	release := make(chan struct{})
	partial := strings.Repeat("sedang menulis. ", 40)
	hm.onStream = func(_ context.Context, emit func(hermes.Event) error) error {
		if err := emit(hermes.Event{Kind: "delta", Text: partial, RunID: "run_partial"}); err != nil {
			return err
		}
		close(first)
		<-release
		if err := emit(hermes.Event{Kind: "done", Text: partial + "\n\nSTATUS: DONE — selesai", RunID: "run_partial"}); err != nil {
			return err
		}
		return nil
	}

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		req := httptest.NewRequest(http.MethodPost,
			"/api/rooms/"+room.ID.String()+"/messages",
			strings.NewReader(`{"text":"mulai"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		e.ServeHTTP(httptest.NewRecorder(), req)
	}()

	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("the turn never streamed its first delta")
	}

	deadline := time.Now().Add(2 * time.Second)
	var streaming store.Message
	for time.Now().Before(deadline) {
		rows := st.messagesFor(room.ID)
		for _, row := range rows {
			if row.Role == "assistant" && row.State == "streaming" {
				streaming = row
			}
		}
		if strings.Contains(streaming.Body, "sedang menulis") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if streaming.ID == uuid.Nil {
		t.Fatal("no streaming assistant row was created at turn start")
	}
	if !strings.Contains(streaming.Body, "sedang menulis") {
		t.Fatalf("streaming body was not persisted: %q", streaming.Body)
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish")
	}

	var final store.Message
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "assistant" {
			final = row
		}
	}
	if final.State != "done" {
		t.Fatalf("final state = %q, want done", final.State)
	}
	if !strings.Contains(final.Body, "sedang menulis") {
		t.Fatalf("final body lost the answer: %q", final.Body)
	}
	if strings.Contains(final.Body, "STATUS:") || strings.Contains(final.Body, " -- ") {
		t.Fatalf("final body was not cleaned before storage: %q", final.Body)
	}
}

func TestRestartMarksStreamingRowsInterruptedAndNotesIt(t *testing.T) {
	st := newFakeStore()
	roomID := uuid.New()
	st.rooms[roomID] = store.Room{
		ID: roomID, Name: "P10 restart", HermesSessionID: "api_p10_restart",
		CreatedAt: time.Now(), FolderPath: t.TempDir(),
	}
	st.messages = append(st.messages, store.Message{
		ID: uuid.New(), RoomID: roomID, Role: "assistant", Body: "setengah kalimat",
		CreatedAt: time.Now(), Meta: []byte("{}"), State: "streaming",
	})
	st.runs["run_died"] = store.Run{
		RunID: "run_died", RoomID: roomID, Status: "running",
		StartedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now(),
	}

	_ = newTestApp(t, st, &fakeHermes{})

	var assistant store.Message
	var note string
	for _, row := range st.messagesFor(roomID) {
		if row.Role == "assistant" {
			assistant = row
		}
		if row.Role == "system" && strings.Contains(row.Body, "terputus") {
			note = row.Body
		}
	}
	if assistant.State != "interrupted" {
		t.Fatalf("assistant state = %q, want interrupted", assistant.State)
	}
	if note == "" {
		t.Fatal("no visible terputus note was written on boot")
	}
	if run := st.runs["run_died"]; run.Status != "interrupted" {
		t.Fatalf("run status = %q, want interrupted", run.Status)
	}
}

func TestRoomPayloadCarriesTheActiveRunAndClearsWhenDone(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_p10_room_run"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	started := time.Now().Add(-42 * time.Second)
	st.mu.Lock()
	st.runs["run_room"] = store.Run{
		RunID: "run_room", RoomID: room.ID, Status: "running",
		StartedAt: started, UpdatedAt: started,
	}
	st.mu.Unlock()

	rec := request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String(), nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("get room = %d, body %s", rec.Code, rec.Body.String())
	}
	var one struct {
		Room roomView `json:"room"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatalf("decode room: %v", err)
	}
	if one.Room.Run == nil || one.Room.Run.ID != "run_room" || one.Room.Run.Status != "running" {
		t.Fatalf("room run = %+v", one.Room.Run)
	}
	if delta := time.Since(one.Room.Run.StartedAt); delta < 41*time.Second || delta > 60*time.Second {
		t.Fatalf("run started_at = %s, want the real start", one.Room.Run.StartedAt)
	}

	rec = request(t, e, http.MethodGet, "/api/rooms", nil, cookie)
	var list struct {
		Rooms []roomView `json:"rooms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode rooms: %v", err)
	}
	if len(list.Rooms) != 1 || list.Rooms[0].Run == nil || list.Rooms[0].Run.Status != "running" {
		t.Fatalf("room list run = %+v", list.Rooms)
	}

	st.mu.Lock()
	run := st.runs["run_room"]
	run.Status = "completed"
	st.runs["run_room"] = run
	st.mu.Unlock()

	rec = request(t, e, http.MethodGet, "/api/rooms", nil, cookie)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode rooms after finish: %v", err)
	}
	if list.Rooms[0].Run != nil {
		t.Fatalf("finished room still carries run = %+v", list.Rooms[0].Run)
	}
}
