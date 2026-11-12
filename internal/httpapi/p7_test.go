package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/auth"
	"github.com/aryadhira/iris/internal/live"
	"github.com/aryadhira/iris/internal/store"
	"github.com/aryadhira/iris/internal/webpush"
)

type pushCall struct {
	RoomID  uuid.UUID
	Payload webpush.Payload
	Exempt  bool
}

type fakePush struct {
	mu    sync.Mutex
	key   string
	calls []pushCall
}

func (f *fakePush) PublicKey() string { return f.key }

func (f *fakePush) Notify(_ context.Context, roomID uuid.UUID, payload webpush.Payload, exempt bool) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, pushCall{RoomID: roomID, Payload: payload, Exempt: exempt})
	return 1, nil
}

func (f *fakePush) all() []pushCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushCall(nil), f.calls...)
}

func p7Server(st *fakeStore, now time.Time, boot time.Time, push PushNotifier) *Server {
	return &Server{
		store:            st,
		logger:           testLogger(),
		now:              func() time.Time { return now },
		bootTime:         boot,
		reminderLoc:      time.FixedZone("WIB", 7*60*60),
		hub:              live.New(),
		push:             push,
		reminderInterval: 30 * time.Second,
	}
}

func seedP7Room(t *testing.T, st *fakeStore, name string) store.Room {
	t.Helper()
	row, err := st.CreateRoom(context.Background(), store.CreateRoomParams{
		ID: uuid.New(), Name: name, HermesSessionID: "api_" + name,
	})
	if err != nil {
		t.Fatalf("seed room: %v", err)
	}
	return row
}

func seedReminder(t *testing.T, st *fakeStore, room store.Room, text string, due time.Time) store.Reminder {
	t.Helper()
	row, err := st.CreateReminder(context.Background(), store.CreateReminderParams{
		ID: uuid.New(), RoomID: room.ID, Text: text, DueAt: due,
	})
	if err != nil {
		t.Fatalf("seed reminder: %v", err)
	}
	return row
}

func TestReminderSchedulerFiresOnceAndPushes(t *testing.T) {
	st := newFakeStore()
	room := seedP7Room(t, st, "Kerja")
	base := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	row := seedReminder(t, st, room, "cek oven", base.Add(time.Minute))
	push := &fakePush{key: "public"}
	s := p7Server(st, base.Add(90*time.Second), base, push)

	s.runDueReminders(context.Background())
	s.runDueReminders(context.Background())

	messages := st.messagesFor(room.ID)
	if len(messages) != 1 {
		t.Fatalf("system messages = %d, want exactly 1", len(messages))
	}
	if !strings.Contains(messages[0].Body, "⏰ Pengingat: cek oven") {
		t.Fatalf("message = %q", messages[0].Body)
	}
	if strings.Contains(messages[0].Body, "terlambat") {
		t.Fatalf("on-time reminder claims it is late: %q", messages[0].Body)
	}
	if st.reminders[0].State != "fired" || st.reminders[0].FiredAt == nil {
		t.Fatalf("reminder after fire = %+v", st.reminders[0])
	}
	calls := push.all()
	if len(calls) != 1 || calls[0].RoomID != room.ID || calls[0].Exempt {
		t.Fatalf("push calls = %+v, want one non-exempt room push", calls)
	}
	if calls[0].Payload.Tag != room.ID.String() || !strings.Contains(calls[0].Payload.Body, "cek oven") {
		t.Fatalf("payload = %+v", calls[0].Payload)
	}
	_ = row
}

func TestReminderMissedWhileDownFiresLateOnBoot(t *testing.T) {
	st := newFakeStore()
	room := seedP7Room(t, st, "Kerja")
	base := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	seedReminder(t, st, room, "telepon Budi", base)
	push := &fakePush{key: "public"}
	// The process started ten minutes after the due time.
	s := p7Server(st, base.Add(10*time.Minute), base.Add(10*time.Minute), push)

	s.runDueReminders(context.Background())

	messages := st.messagesFor(room.ID)
	if len(messages) != 1 {
		t.Fatalf("system messages = %d, want 1", len(messages))
	}
	body := messages[0].Body
	if !strings.Contains(body, "(terlambat 10 menit)") {
		t.Fatalf("late message = %q, want (terlambat 10 menit)", body)
	}
	if len(push.all()) != 1 {
		t.Fatalf("late reminder did not push")
	}
}

func TestCancelledReminderNeverFires(t *testing.T) {
	st := newFakeStore()
	room := seedP7Room(t, st, "Kerja")
	base := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	row := seedReminder(t, st, room, "jangan bunyi", base)
	if _, err := st.CancelReminder(context.Background(), store.CancelReminderParams{ID: row.ID, RoomID: room.ID}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s := p7Server(st, base.Add(time.Hour), base, &fakePush{key: "public"})

	s.runDueReminders(context.Background())

	if len(st.messagesFor(room.ID)) != 0 {
		t.Fatalf("a cancelled reminder posted a message")
	}
	if st.reminders[0].State != "cancelled" {
		t.Fatalf("state = %q, want cancelled", st.reminders[0].State)
	}
}

func TestTypedMessageCreatesReminderAndEmitsToastEvent(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_reminder"}
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	frames := sendTurn(t, e, cookie, room.ID, "ingatkan saya dalam 1 menit cek p7")

	var reminderFrame *reminderView
	for _, frame := range frames {
		if frame.Name == "reminder" {
			var view reminderView
			if err := json.Unmarshal([]byte(frame.Data), &view); err != nil {
				t.Fatalf("decode reminder frame: %v", err)
			}
			reminderFrame = &view
		}
	}
	if reminderFrame == nil {
		t.Fatalf("no reminder event in %+v", frames)
	}
	if reminderFrame.Text != "cek p7" {
		t.Fatalf("reminder text = %q, want cek p7", reminderFrame.Text)
	}
	if !reminderFrame.DueAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("due = %s, want %s", reminderFrame.DueAt, now.Add(time.Minute))
	}
	if len(st.reminders) != 1 || st.reminders[0].State != "pending" {
		t.Fatalf("stored reminders = %+v", st.reminders)
	}
}

func TestTypedMessageWithoutExplicitTimeCreatesNothing(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_reminder"}
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	e := newP6App(t, st, hm, func() time.Time { return now }, 5*time.Minute)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	frames := sendTurn(t, e, cookie, room.ID, "tolong ingatkan aku soal rapat")
	for _, frame := range frames {
		if frame.Name == "reminder" {
			t.Fatalf("a reminder was created without an explicit time: %+v", frame)
		}
	}
	if len(st.reminders) != 0 {
		t.Fatalf("stored reminders = %+v, want none", st.reminders)
	}
}

func TestPushSubscriptionRequiresOwnerAndARealRoom(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_push"}
	push := &fakePush{key: "test-public-key"}
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	e := New(Options{
		Store: st, Hermes: hm, Auth: loginAuth(st), Logger: testLogger(), Version: "test",
		DataDir: t.TempDir(), WebDir: t.TempDir(), Now: func() time.Time { return now }, Push: push,
	})
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	body := map[string]any{
		"room_id": room.ID.String(), "endpoint": "http://127.0.0.1:9338/push/abc",
		"keys": map[string]string{"p256dh": "p256dh-value", "auth": "auth-value"},
	}

	rec := request(t, e, http.MethodPost, "/api/push/subscribe", body, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("subscribe without cookie = %d, want 401", rec.Code)
	}

	missing := map[string]any{
		"room_id": uuid.NewString(), "endpoint": "http://127.0.0.1:9338/push/abc",
		"keys": map[string]string{"p256dh": "p256dh-value", "auth": "auth-value"},
	}
	rec = request(t, e, http.MethodPost, "/api/push/subscribe", missing, cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("subscribe to a missing room = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}

	rec = request(t, e, http.MethodPost, "/api/push/subscribe", body, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe = %d, body %s", rec.Code, rec.Body.String())
	}
	if len(st.pushSubs) != 1 || st.pushSubs[0].RoomID != room.ID {
		t.Fatalf("stored subscriptions = %+v", st.pushSubs)
	}

	rec = request(t, e, http.MethodGet, "/api/push/key", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "test-public-key") {
		t.Fatalf("push key = %d %s", rec.Code, rec.Body.String())
	}

	rec = request(t, e, http.MethodDelete, "/api/push/subscribe",
		map[string]string{"endpoint": body["endpoint"].(string)}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("unsubscribe = %d, body %s", rec.Code, rec.Body.String())
	}
	if len(st.pushSubs) != 0 {
		t.Fatalf("subscription survived DELETE: %+v", st.pushSubs)
	}
}

func TestApprovalPushIsAlwaysExemptFromDebounce(t *testing.T) {
	st := newFakeStore()
	push := &fakePush{key: "public"}
	room := seedP7Room(t, st, "Kerja")
	s := p7Server(st, time.Now(), time.Now(), push)
	s.notifyApproval(context.Background(), room, store.Approval{
		Command: "rtp --danger", Description: "mengubah konfigurasi",
	})
	calls := push.all()
	if len(calls) != 1 || !calls[0].Exempt {
		t.Fatalf("approval push calls = %+v, want one exempt call", calls)
	}
	if !strings.Contains(calls[0].Payload.Body, "rtp --danger") {
		t.Fatalf("approval body = %q", calls[0].Payload.Body)
	}
}

// loginAuth builds the same auth service the test app uses.
func loginAuth(st *fakeStore) *auth.Service {
	return auth.New(st, testPassphrase, false, testLogger())
}
