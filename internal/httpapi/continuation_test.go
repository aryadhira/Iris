package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/continuation"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/store"
)

var testStart = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func newContinuationServer(st *fakeStore, hm *fakeHermes, clock *time.Time) *Server {
	return &Server{
		store:                st,
		hermes:               hm,
		logger:               testLogger(),
		now:                  func() time.Time { return *clock },
		continuationInterval: time.Hour,
	}
}

func seedRoom(st *fakeStore, archived bool) store.Room {
	room := store.Room{
		ID:              uuid.New(),
		Name:            "Continuity",
		HermesSessionID: "api_p3",
		FolderPath:      "/tmp/iris-p3-room",
		SystemPrompt:    "contract",
		CreatedAt:       testStart,
	}
	if archived {
		archivedAt := testStart
		room.ArchivedAt = &archivedAt
	}
	st.mu.Lock()
	st.rooms[room.ID] = room
	st.mu.Unlock()
	return room
}

func seedAssistant(t *testing.T, st *fakeStore, room store.Room, at time.Time, meta continuation.Meta) store.Message {
	t.Helper()
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.InsertMessage(context.Background(), store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Role:   "assistant",
		Body:   "pekerjaan latar dimulai",
		Meta:   raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	for i := range st.messages {
		if st.messages[i].ID == row.ID {
			st.messages[i].CreatedAt = at
		}
	}
	st.mu.Unlock()
	row.CreatedAt = at
	return row
}

func TestContinuationSchedulesOnRunningButNeverOnDone(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want int
	}{
		{"RUNNING", "STATUS: RUNNING — menunggu 90 detik", 1},
		{"DONE", "STATUS: DONE — selesai", 0},
		{"BLOCKED", "STATUS: BLOCKED — butuh izin", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore()
			room := seedRoom(st, false)
			root := seedAssistant(t, st, room, testStart, continuation.Meta{})
			clock := testStart
			srv := newContinuationServer(st, &fakeHermes{}, &clock)

			srv.scheduleAfterTurn(context.Background(), room, chainRef{
				id:        root.ID,
				createdAt: root.CreatedAt,
				deadline:  root.CreatedAt.Add(continuation.Window),
			}, false, continuation.Meta{
				Procs:      []string{"proc_abc123"},
				Tools:      []continuation.Tool{{Name: "terminal", Status: "completed"}},
				StatusLine: tc.line,
			})

			st.mu.Lock()
			got := len(st.continuations)
			st.mu.Unlock()
			if got != tc.want {
				t.Fatalf("continuations = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestContinuationStopsAfterThree(t *testing.T) {
	st := newFakeStore()
	room := seedRoom(st, false)
	root := seedAssistant(t, st, room, testStart, continuation.Meta{})
	clock := testStart
	srv := newContinuationServer(st, &fakeHermes{}, &clock)
	chain := chainRef{id: root.ID, createdAt: root.CreatedAt, deadline: root.CreatedAt.Add(continuation.Window)}
	meta := continuation.Meta{
		Procs:      []string{"proc_abc123"},
		Tools:      []continuation.Tool{{Name: "process_manage", Status: "completed"}},
		StatusLine: "STATUS: RUNNING — masih jalan",
	}

	srv.scheduleAfterTurn(context.Background(), room, chain, false, meta)
	for i := 0; i < 2; i++ {
		srv.scheduleAfterTurn(context.Background(), room, chain, true, meta)
	}

	st.mu.Lock()
	rows := append([]store.Continuation(nil), st.continuations...)
	st.mu.Unlock()
	if len(rows) != continuation.MaxPerChain {
		t.Fatalf("continuations = %d, want %d", len(rows), continuation.MaxPerChain)
	}

	// The fourth decision is capped: no row, one visible reason.
	srv.scheduleAfterTurn(context.Background(), room, chain, true, meta)
	st.mu.Lock()
	after := len(st.continuations)
	st.mu.Unlock()
	if after != continuation.MaxPerChain {
		t.Fatalf("a capped chain created another row: %d", after)
	}
	var cappedVisible bool
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "system" && strings.Contains(row.Body, "batas 3 lanjutan") {
			cappedVisible = true
		}
	}
	if !cappedVisible {
		t.Fatal("the cap reason is not visible in the room timeline")
	}
}

func TestContinuationCapsAtThirtyMinutesWithFakeClock(t *testing.T) {
	st := newFakeStore()
	room := seedRoom(st, false)
	root := seedAssistant(t, st, room, testStart, continuation.Meta{})
	clock := testStart
	srv := newContinuationServer(st, &fakeHermes{}, &clock)
	chain := chainRef{id: root.ID, createdAt: root.CreatedAt, deadline: root.CreatedAt.Add(continuation.Window)}

	clock = testStart.Add(continuation.Window + time.Second)
	srv.scheduleAfterTurn(context.Background(), room, chain, false, continuation.Meta{
		Procs:      []string{"proc_abc123"},
		Tools:      []continuation.Tool{{Name: "terminal", Status: "completed"}},
		StatusLine: "STATUS: RUNNING — masih jalan",
	})

	st.mu.Lock()
	got := len(st.continuations)
	st.mu.Unlock()
	if got != 0 {
		t.Fatalf("continuation created after the 30 minute cap: %d", got)
	}
	var visible bool
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "system" && strings.Contains(row.Body, "batas 30 menit") {
			visible = true
		}
	}
	if !visible {
		t.Fatal("the 30 minute reason is not visible in the room timeline")
	}
}

func pendingRow(t *testing.T, st *fakeStore, room store.Room, chainID uuid.UUID, deadline time.Time) store.Continuation {
	t.Helper()
	row, err := st.CreateContinuation(context.Background(), store.CreateContinuationParams{
		ID:       uuid.New(),
		RoomID:   room.ID,
		ChainID:  chainID,
		Attempt:  1,
		DueAt:    testStart,
		Deadline: deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestContinuationCancelsOnANewerUserMessage(t *testing.T) {
	st := newFakeStore()
	room := seedRoom(st, false)
	root := seedAssistant(t, st, room, testStart, continuation.Meta{
		Procs:      []string{"proc_abc123"},
		StatusLine: "STATUS: RUNNING — 90 detik",
	})
	row := pendingRow(t, st, room, root.ID, root.CreatedAt.Add(continuation.Window))
	if _, err := st.InsertMessage(context.Background(), store.InsertMessageParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Role:   "user",
		Body:   "sudah selesai?",
		Meta:   []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	// Make the user message newer than the chain root explicitly.
	st.mu.Lock()
	st.messages[len(st.messages)-1].CreatedAt = testStart.Add(time.Second)
	st.mu.Unlock()

	hm := &fakeHermes{}
	clock := testStart.Add(time.Minute)
	srv := newContinuationServer(st, hm, &clock)
	srv.processContinuation(context.Background(), row)

	st.mu.Lock()
	hmCalls := hm.streamCalls
	status := st.continuations[0].Status
	st.mu.Unlock()
	if hmCalls != 0 {
		t.Fatal("a cancelled continuation still called Hermes")
	}
	if status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", status)
	}
	var visible bool
	for _, msg := range st.messagesFor(room.ID) {
		if msg.Role == "system" && strings.Contains(msg.Body, "ada pesan baru dari Boss") {
			visible = true
		}
	}
	if !visible {
		t.Fatal("the cancellation reason is not visible in the room timeline")
	}
}

func TestContinuationCancelsWhenTheRoomIsArchived(t *testing.T) {
	st := newFakeStore()
	room := seedRoom(st, true)
	root := seedAssistant(t, st, room, testStart, continuation.Meta{Procs: []string{"proc_abc123"}})
	row := pendingRow(t, st, room, root.ID, root.CreatedAt.Add(continuation.Window))
	hm := &fakeHermes{}
	clock := testStart.Add(time.Minute)
	srv := newContinuationServer(st, hm, &clock)

	srv.processContinuation(context.Background(), row)

	st.mu.Lock()
	status := st.continuations[0].Status
	reason := st.continuations[0].Reason
	st.mu.Unlock()
	if status != "cancelled" || !strings.Contains(reason, "diarsipkan") {
		t.Fatalf("continuation = %q / %q, want cancelled because archived", status, reason)
	}
}

func TestRestartResumesAPendingChain(t *testing.T) {
	st := newFakeStore()
	room := seedRoom(st, false)
	root := seedAssistant(t, st, room, testStart, continuation.Meta{Procs: []string{"proc_abc123"}})
	row := pendingRow(t, st, room, root.ID, root.CreatedAt.Add(continuation.Window))

	// Simulate a crash mid-wait: the row was claimed but never finished.
	st.mu.Lock()
	st.continuations[0].Status = "running"
	started := testStart
	st.continuations[0].StartedAt = &started
	st.mu.Unlock()

	called := make(chan string, 2)
	hm := &fakeHermes{
		onStream: func(_ context.Context, emit func(hermes.Event) error) error {
			called <- "hermes"
			return emit(hermes.Event{Kind: "delta", Text: "hasil akhir\nSTATUS: DONE — selesai"})
		},
	}
	clock := testStart.Add(time.Minute)
	srv := newContinuationServer(st, hm, &clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartContinuations(ctx)

	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("a chain interrupted by a restart was not resumed")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		status := st.continuations[0].Status
		st.mu.Unlock()
		if status == "done" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("resumed continuation did not finish (row %s)", row.ID)
}

func TestStreamStoresProcMetaAndSchedulesContinuation(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_stream"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	hm.events = []hermes.Event{
		{Kind: "tool", Name: "terminal", Status: "started", TS: 100},
		{Kind: "tool", Name: "terminal", Status: "completed", TS: 100.75},
		{Kind: "delta", Text: "Sudah jalan, proses proc_6a1c1303b005.\n\nSTATUS: RUNNING — 90 detik"},
		{Kind: "done", Text: "Sudah jalan, proses proc_6a1c1303b005.\n\nSTATUS: RUNNING — 90 detik"},
	}

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "mulai"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	st.mu.Lock()
	var assistant store.Message
	for _, row := range st.messages {
		if row.RoomID == room.ID && row.Role == "assistant" {
			assistant = row
		}
	}
	var rows []store.Continuation
	for _, row := range st.continuations {
		if row.RoomID == room.ID {
			rows = append(rows, row)
		}
	}
	st.mu.Unlock()

	meta := parseMeta(assistant.Meta)
	if len(meta.Procs) != 1 || meta.Procs[0] != "proc_6a1c1303b005" {
		t.Fatalf("stored procs = %v, want [proc_6a1c1303b005]", meta.Procs)
	}
	if len(meta.Tools) != 1 || meta.Tools[0].Name != "terminal" || meta.Tools[0].DurationMs != 750 || meta.Tools[0].Status != "completed" {
		t.Fatalf("stored tools = %+v", meta.Tools)
	}
	if meta.StatusLine != "STATUS: RUNNING — 90 detik" {
		t.Fatalf("status line = %q", meta.StatusLine)
	}
	if len(rows) != 1 || rows[0].Attempt != 1 || rows[0].Status != "pending" {
		t.Fatalf("scheduled continuations = %+v, want one pending attempt 1", rows)
	}
}

func TestContinuationSkippedWhenThePreviousAttemptUsedNoTools(t *testing.T) {
	st := newFakeStore()
	room := seedRoom(st, false)
	root := seedAssistant(t, st, room, testStart, continuation.Meta{})
	pendingRow(t, st, room, root.ID, root.CreatedAt.Add(continuation.Window))
	clock := testStart
	srv := newContinuationServer(st, &fakeHermes{}, &clock)

	srv.scheduleAfterTurn(context.Background(), room, chainRef{
		id:        root.ID,
		createdAt: root.CreatedAt,
		deadline:  root.CreatedAt.Add(continuation.Window),
	}, true, continuation.Meta{StatusLine: "STATUS: RUNNING — masih jalan"})

	st.mu.Lock()
	rows := len(st.continuations)
	st.mu.Unlock()
	if rows != 1 {
		t.Fatalf("a no-tool continuation created another row: %d", rows)
	}
	var visible bool
	for _, msg := range st.messagesFor(room.ID) {
		if msg.Role == "system" && strings.Contains(msg.Body, "tidak memakai alat") {
			visible = true
		}
	}
	if !visible {
		t.Fatal("the no-activity skip reason is not visible")
	}
}

func TestContinuationEndpointsExposeAndCancel(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_http"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)
	root := seedAssistant(t, st, room, testStart, continuation.Meta{Procs: []string{"proc_abc123"}})
	pendingRow(t, st, room, root.ID, root.CreatedAt.Add(continuation.Window))

	rec := request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/continuation", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET continuation status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Continuation *continuationView `json:"continuation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Continuation == nil || got.Continuation.Attempt != 1 || got.Continuation.Status != "pending" {
		t.Fatalf("continuation = %+v", got.Continuation)
	}

	rec = request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/continuation/cancel", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var cancelled struct {
		Cancelled int `json:"cancelled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1", cancelled.Cancelled)
	}

	rec = request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/continuation", nil, cookie)
	if !strings.Contains(rec.Body.String(), `"continuation":null`) {
		t.Fatalf("after cancel: %s", rec.Body.String())
	}
}

func TestCancellationDuringARunningContinuationIsNotOverwritten(t *testing.T) {
	st := newFakeStore()
	room := seedRoom(st, false)
	root := seedAssistant(t, st, room, testStart, continuation.Meta{Procs: []string{"proc_abc123"}})
	_ = pendingRow(t, st, room, root.ID, root.CreatedAt.Add(continuation.Window))

	clock := testStart.Add(time.Minute)
	srv := newContinuationServer(st, nil, &clock)
	now := clock
	claimed, err := st.ClaimDueContinuations(context.Background(), store.ClaimDueContinuationsParams{StartedAt: &now, Limit: 5})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v %v", claimed, err)
	}

	// The user types while the continuation is inside its Hermes turn.
	srv.hermes = &fakeHermes{onStream: func(_ context.Context, emit func(hermes.Event) error) error {
		_, _ = st.CancelPendingContinuationsForRoom(context.Background(), store.CancelPendingContinuationsForRoomParams{
			RoomID: room.ID, Reason: "ada pesan baru dari Boss", FinishedAt: &now,
		})
		return emit(hermes.Event{Kind: "delta", Text: "hasil lama\n\nSTATUS: DONE — sudah selesai"})
	}}

	srv.processContinuation(context.Background(), claimed[0])

	st.mu.Lock()
	status := st.continuations[0].Status
	reason := st.continuations[0].Reason
	count := len(st.continuations)
	st.mu.Unlock()
	if status != "cancelled" || !strings.Contains(reason, "pesan baru") {
		t.Fatalf("row = %q / %q, want the cancellation preserved", status, reason)
	}
	if count != 1 {
		t.Fatalf("a cancelled chain scheduled another row: %d", count)
	}
}
