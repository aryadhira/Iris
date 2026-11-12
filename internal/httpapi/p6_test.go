package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/auth"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/store"
)

func newP6App(t *testing.T, st *fakeStore, hm hermes.Client, now func() time.Time, ttl time.Duration) *echo.Echo {
	t.Helper()
	logger := testLogger()
	return New(Options{
		Store:           st,
		Hermes:          hm,
		Auth:            auth.New(st, testPassphrase, false, logger),
		Logger:          logger,
		Version:         "test",
		DataDir:         t.TempDir(),
		WebDir:          t.TempDir(),
		Now:             now,
		ApprovalTimeout: ttl,
	})
}

func approvalEvents() []hermes.Event {
	return []hermes.Event{
		{Kind: "started", OriginalKind: "run.started", SessionID: "api_test_room", RunID: "run_1"},
		{Kind: "tool", OriginalKind: "tool.started", RunID: "run_1", Name: "terminal", Status: "started"},
		{
			Kind: "approval", OriginalKind: "approval.request", RunID: "run_1",
			SessionID: "api_test_room", RequestID: "req_1", Name: "terminal",
			Command:     "rtk curl -s --max-time 2 http://169.254.169.254/latest/meta-data/",
			Description: "cloud metadata endpoint access (instance credentials)",
			Choices:     []string{"once", "deny"},
			Raw:         []byte(`{"request_id":"req_1","choices":["once","deny"]}`),
		},
		{Kind: "delta", OriginalKind: "assistant.delta", RunID: "run_1", Text: "selesai"},
		{Kind: "done", OriginalKind: "run.completed", RunID: "run_1", Text: "selesai"},
	}
}

func sendTurn(t *testing.T, e *echo.Echo, cookie *http.Cookie, roomID uuid.UUID, text string) []sseEvent {
	t.Helper()
	rec := request(t, e, http.MethodPost, "/api/rooms/"+roomID.String()+"/messages", map[string]string{"text": text}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("send turn = %d, body %s", rec.Code, rec.Body.String())
	}
	return parseSSE(t, rec.Body.String())
}

func TestApprovalLifecyclePersistsRunAndResolvesOnce(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{events: approvalEvents()}
	fixed := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return fixed }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	frames := sendTurn(t, e, cookie, room.ID, "jalankan probe")
	var approvalFrame *approvalView
	for _, frame := range frames {
		if frame.Name == "approval" {
			var view approvalView
			if err := json.Unmarshal([]byte(frame.Data), &view); err != nil {
				t.Fatalf("decode approval frame: %v", err)
			}
			approvalFrame = &view
		}
	}
	if approvalFrame == nil {
		t.Fatalf("no approval SSE frame in %+v", frames)
	}
	if approvalFrame.Command != "rtk curl -s --max-time 2 http://169.254.169.254/latest/meta-data/" {
		t.Errorf("command = %q", approvalFrame.Command)
	}
	if approvalFrame.ToolName != "terminal" {
		t.Errorf("tool name = %q, want terminal", approvalFrame.ToolName)
	}
	if len(approvalFrame.Choices) != 2 || approvalFrame.Choices[0] != "once" || approvalFrame.Choices[1] != "deny" {
		t.Errorf("choices = %v", approvalFrame.Choices)
	}
	if approvalFrame.State != "pending" {
		t.Errorf("state = %q, want pending", approvalFrame.State)
	}

	roomsRec := request(t, e, http.MethodGet, "/api/rooms", nil, cookie)
	var roomsBody struct {
		Rooms []roomView `json:"rooms"`
	}
	if err := json.Unmarshal(roomsRec.Body.Bytes(), &roomsBody); err != nil {
		t.Fatalf("decode rooms: %v", err)
	}
	if len(roomsBody.Rooms) != 1 || roomsBody.Rooms[0].PendingApprovals != 1 {
		t.Errorf("pending approval count = %+v, want 1", roomsBody.Rooms)
	}

	rows := st.approvalsFor(room.ID)
	if len(rows) != 1 {
		t.Fatalf("approval rows = %d, want 1", len(rows))
	}
	if rows[0].RequestID != "req_1" || rows[0].RunID != "run_1" {
		t.Errorf("stored approval = %+v", rows[0])
	}
	if !rows[0].ExpiresAt.Equal(fixed.Add(5 * time.Minute)) {
		t.Errorf("expires_at = %s, want %s", rows[0].ExpiresAt, fixed.Add(5*time.Minute))
	}
	if run, ok := st.runs["run_1"]; !ok || run.SessionID != "api_test_room" {
		t.Errorf("run row = %+v, want session api_test_room", run)
	}

	// Run identity must be on the assistant message, not only in a log.
	var assistant *store.Message
	for _, message := range st.messagesFor(room.ID) {
		if message.Role == "assistant" {
			copy := message
			assistant = &copy
		}
	}
	if assistant == nil || assistant.RunID == nil || *assistant.RunID != "run_1" {
		t.Fatalf("assistant message run id = %+v, want run_1", assistant)
	}

	rec := request(t, e, http.MethodPost,
		"/api/rooms/"+room.ID.String()+"/approvals/"+approvalFrame.ID,
		map[string]string{"choice": "once"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve = %d, body %s", rec.Code, rec.Body.String())
	}
	if len(hm.resolveCalls) != 1 {
		t.Fatalf("Hermes resolve calls = %d, want 1", len(hm.resolveCalls))
	}
	if hm.resolveCalls[0].RunID != "run_1" || hm.resolveCalls[0].RequestID != "req_1" || hm.resolveCalls[0].Choice != "once" {
		t.Errorf("resolve call = %+v", hm.resolveCalls[0])
	}
	rows = st.approvalsFor(room.ID)
	if rows[0].State != "allowed" || rows[0].Choice == nil || *rows[0].Choice != "once" {
		t.Errorf("resolved approval = %+v", rows[0])
	}
}

func TestApprovalRejectsAChoiceHermesDidNotOffer(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	id := uuid.New()
	_, err := st.UpsertApproval(context.Background(), store.UpsertApprovalParams{
		ID: id, RoomID: room.ID, RunID: "run_1", SessionID: "api_test_room", RequestID: "req_1",
		Command: "redacted command", Choices: []string{"once", "deny"},
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	rec := request(t, e, http.MethodPost,
		"/api/rooms/"+room.ID.String()+"/approvals/"+id.String(),
		map[string]string{"choice": "always"}, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("always not offered = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if len(hm.resolveCalls) != 0 {
		t.Fatalf("Hermes was called for an unoffered choice: %+v", hm.resolveCalls)
	}
	if got := st.approvalsFor(room.ID)[0].State; got != "pending" {
		t.Errorf("state after rejected choice = %q, want pending", got)
	}
}

func TestApprovalExpiresAndLeavesTheRoomWithoutPendingWork(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	id := uuid.New()
	_, err := st.UpsertApproval(context.Background(), store.UpsertApprovalParams{
		ID: id, RoomID: room.ID, RunID: "run_1", SessionID: "api_test_room", RequestID: "req_1",
		Command: "redacted command", Choices: []string{"once", "deny"},
		CreatedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	rec := request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/approvals", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list approvals = %d, body %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Approvals []approvalView `json:"approvals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode approvals: %v", err)
	}
	if len(list.Approvals) != 1 || list.Approvals[0].State != "expired" {
		t.Fatalf("approvals = %+v, want one expired", list.Approvals)
	}

	rec = request(t, e, http.MethodPost,
		"/api/rooms/"+room.ID.String()+"/approvals/"+id.String(),
		map[string]string{"choice": "once"}, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("answer after expiry = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if len(hm.resolveCalls) != 0 {
		t.Fatalf("expired approval was sent to Hermes: %+v", hm.resolveCalls)
	}

	rec = request(t, e, http.MethodGet, "/api/rooms", nil, cookie)
	var roomsBody struct {
		Rooms []roomView `json:"rooms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &roomsBody); err != nil {
		t.Fatalf("decode rooms: %v", err)
	}
	if len(roomsBody.Rooms) != 1 || roomsBody.Rooms[0].PendingApprovals != 0 {
		t.Fatalf("pending approvals = %+v, want 0", roomsBody.Rooms)
	}
}

func TestDeniedApprovalDoesNotArmContinuation(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{events: []hermes.Event{
		{Kind: "started", OriginalKind: "run.started", SessionID: "api_test_room", RunID: "run_1"},
		{Kind: "tool", OriginalKind: "tool.started", RunID: "run_1", Name: "terminal", Status: "started"},
		{Kind: "done", OriginalKind: "run.completed", RunID: "run_1", Text: "proc_abc123 masih berjalan",
			Raw: []byte(`{"content":"proc_abc123 masih berjalan"}`)},
	}}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	_, err := st.UpsertApproval(context.Background(), store.UpsertApprovalParams{
		ID: uuid.New(), RoomID: room.ID, RunID: "run_1", SessionID: "api_test_room", RequestID: "req_1",
		Command: "redacted command", Choices: []string{"once", "deny"},
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("seed approval: %v", err)
	}
	deny := "deny"
	if _, err := st.ResolveApproval(context.Background(), store.ResolveApprovalParams{
		ID: st.approvalsFor(room.ID)[0].ID, State: "denied", Choice: &deny, ResolvedAt: &now,
	}); err != nil {
		t.Fatalf("deny approval: %v", err)
	}

	sendTurn(t, e, cookie, room.ID, "kerja")
	if len(st.continuations) != 0 {
		t.Fatalf("a denied approval armed %d continuation(s)", len(st.continuations))
	}
	if len(hm.resolveCalls) != 0 {
		t.Fatalf("a denied approval was retried with Hermes: %+v", hm.resolveCalls)
	}
}

func TestStopIsIdempotentAndLeavesOneSystemLine(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)
	st.runs["run_live"] = store.Run{RunID: "run_live", RoomID: room.ID, SessionID: "api_test_room", Status: "running"}

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/stop", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop = %d, body %s", rec.Code, rec.Body.String())
	}
	var first struct {
		Stopped int    `json:"stopped"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode stop: %v", err)
	}
	if first.Stopped != 1 {
		t.Fatalf("stopped = %d, want 1", first.Stopped)
	}
	if len(hm.stopCalls) != 1 || hm.stopCalls[0] != "run_live" {
		t.Fatalf("stop calls = %v", hm.stopCalls)
	}
	if st.runs["run_live"].Status != "stopping" {
		t.Errorf("run status = %q, want stopping", st.runs["run_live"].Status)
	}

	rec = request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/stop", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("second stop = %d, body %s", rec.Code, rec.Body.String())
	}
	var second struct {
		Stopped int    `json:"stopped"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second stop: %v", err)
	}
	if second.Stopped != 0 || second.Reason == "" {
		t.Fatalf("second stop = %+v, want 0 with a reason", second)
	}
	if len(hm.stopCalls) != 1 {
		t.Fatalf("second stop called Hermes again: %v", hm.stopCalls)
	}

	count := 0
	for _, message := range st.messagesFor(room.ID) {
		if message.Role == "system" && message.Body == "dihentikan oleh Boss" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("system lines = %d, want exactly 1", count)
	}
}

func TestStopOnAStaleRunAnswers200Not500(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{stopErr: &hermes.APIError{
		StatusCode: http.StatusConflict,
		Body:       `{"error":{"code":"run_not_active","message":"Run is not active"}}`,
	}}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)
	st.runs["run_stale"] = store.Run{RunID: "run_stale", RoomID: room.ID, SessionID: "api_test_room", Status: "running"}

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/stop", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("stale stop = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Stopped int    `json:"stopped"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Stopped != 0 || body.Reason == "" {
		t.Fatalf("stale stop = %+v, want 0 with a reason", body)
	}
	if st.runs["run_stale"].Status != "interrupted" {
		t.Errorf("stale run status = %q, want interrupted", st.runs["run_stale"].Status)
	}
}

func TestApprovalHermes409IsHonestAndNotRetried(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{resolveErr: &hermes.APIError{
		StatusCode: http.StatusConflict,
		Body:       `{"error":{"code":"approval_not_active","message":"Run has no active approval session"}}`,
	}}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	id := uuid.New()
	_, err := st.UpsertApproval(context.Background(), store.UpsertApprovalParams{
		ID: id, RoomID: room.ID, RunID: "run_stale", SessionID: "api_test_room", RequestID: "req_1",
		Command: "redacted command", Choices: []string{"once", "deny"},
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	rec := request(t, e, http.MethodPost,
		"/api/rooms/"+room.ID.String()+"/approvals/"+id.String(),
		map[string]string{"choice": "once"}, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale approval = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "Izinnya sudah tidak berlaku." {
		t.Fatalf("error = %q", body["error"])
	}
	if got := st.approvalsFor(room.ID)[0].State; got != "superseded" {
		t.Errorf("state = %q, want superseded", got)
	}

	rec = request(t, e, http.MethodPost,
		"/api/rooms/"+room.ID.String()+"/approvals/"+id.String(),
		map[string]string{"choice": "once"}, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second answer = %d, want 409", rec.Code)
	}
	if len(hm.resolveCalls) != 1 {
		t.Fatalf("a 409 approval was retried: %+v", hm.resolveCalls)
	}
}

func TestStopSentinelDoesNotOverwriteCancelledWithCompleted(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{events: []hermes.Event{
		{Kind: "started", OriginalKind: "run.started", SessionID: "api_test_room", RunID: "run_stop"},
		{Kind: "tool", OriginalKind: "tool.started", RunID: "run_stop", Name: "terminal", Status: "started"},
		{Kind: "done", OriginalKind: "assistant.completed", RunID: "run_stop", Text: ""},
		{Kind: "cancelled", OriginalKind: "run.cancelled", RunID: "run_stop"},
		{Kind: "done", OriginalKind: "done", RunID: "run_stop"},
	}}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	sendTurn(t, e, cookie, room.ID, "kerja panjang")
	run, ok := st.runs["run_stop"]
	if !ok {
		t.Fatal("run row was not recorded")
	}
	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled (the done sentinel must not win)", run.Status)
	}
	if len(st.continuations) != 0 {
		t.Fatalf("a cancelled run armed %d continuation(s)", len(st.continuations))
	}
}

func TestApprovalResolveRequiresTheOwnerCookie(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	id := uuid.New()
	if _, err := st.UpsertApproval(context.Background(), store.UpsertApprovalParams{
		ID: id, RoomID: room.ID, RunID: "run_1", SessionID: "api_test_room", RequestID: "req_1",
		Command: "redacted command", Choices: []string{"once", "deny"},
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("seed approval: %v", err)
	}

	rec := request(t, e, http.MethodPost,
		"/api/rooms/"+room.ID.String()+"/approvals/"+id.String(),
		map[string]string{"choice": "once"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated answer = %d, want 401", rec.Code)
	}
	if len(hm.resolveCalls) != 0 {
		t.Fatalf("unauthenticated answer reached Hermes: %+v", hm.resolveCalls)
	}
}
