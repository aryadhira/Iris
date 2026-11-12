package live

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func receive(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return Event{}
}

func TestFanOutReachesTwoSubscribers(t *testing.T) {
	h := New()
	room := uuid.New()

	snapA, chA, cancelA := h.Subscribe(room)
	defer cancelA()
	snapB, chB, cancelB := h.Subscribe(room)
	defer cancelB()
	if snapA.Partial != "" || snapB.Partial != "" {
		t.Fatalf("fresh snapshots partial = %q / %q, want empty", snapA.Partial, snapB.Partial)
	}

	h.Append(room, "ha")
	h.Append(room, "lo")
	h.Publish(room, Event{Name: "done", Payload: map[string]string{"messageId": "m1"}})

	for _, ch := range []<-chan Event{chA, chB} {
		first := receive(t, ch)
		second := receive(t, ch)
		done := receive(t, ch)
		if first.Name != "delta" || first.Payload.(map[string]string)["text"] != "ha" {
			t.Fatalf("first event = %+v", first)
		}
		if second.Name != "delta" || second.Payload.(map[string]string)["text"] != "lo" {
			t.Fatalf("second event = %+v", second)
		}
		if done.Name != "done" {
			t.Fatalf("third event = %+v, want done", done)
		}
	}
	if got := h.Partial(room); got != "halo" {
		t.Fatalf("partial = %q, want halo", got)
	}
}

func TestSnapshotIsAtomicWithTheDeltaBoundary(t *testing.T) {
	h := New()
	room := uuid.New()
	h.Append(room, "sudah ")

	snap, ch, cancel := h.Subscribe(room)
	defer cancel()
	if snap.Partial != "sudah " {
		t.Fatalf("snapshot partial = %q, want %q", snap.Partial, "sudah ")
	}
	h.Append(room, "jalan")
	ev := receive(t, ch)
	if ev.Payload.(map[string]string)["text"] != "jalan" {
		t.Fatalf("delta after snapshot = %+v", ev)
	}
	if got := snap.Partial + ev.Payload.(map[string]string)["text"]; got != "sudah jalan" {
		t.Fatalf("snapshot + delta = %q", got)
	}
}

func TestSlowSubscriberIsDroppedAndNeverBlocksPublish(t *testing.T) {
	h := NewWithBuffer(1)
	room := uuid.New()
	_, slow, cancelSlow := h.Subscribe(room)
	defer cancelSlow()
	_, fast, cancelFast := h.Subscribe(room)
	defer cancelFast()

	// Fill the slow queue. Its reader is not reading, exactly like a phone whose
	// radio went away without a FIN.
	h.Publish(room, Event{Name: "tool", Payload: "first"})
	// The fast reader stays current, so only the dead one overflows.
	if first := receive(t, fast); first.Name != "tool" {
		t.Fatalf("fast first = %+v", first)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Append(room, "this must not block")
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("publisher blocked on a slow subscriber")
	}
	if delta := receive(t, fast); delta.Name != "delta" {
		t.Fatalf("fast delta = %+v", delta)
	}
	h.Publish(room, Event{Name: "done"})
	doneEv := receive(t, fast)
	if doneEv.Name != "done" {
		t.Fatalf("fast done = %+v", doneEv)
	}
	// The slow subscriber was removed and its channel closed, not left open.
	for range slow {
	}
	if got := h.Count(room); got != 1 {
		t.Fatalf("subscribers = %d, want only the fast one", got)
	}
}

func TestPublishDoesNotBlockWithNoReader(t *testing.T) {
	h := NewWithBuffer(1)
	room := uuid.New()
	_, _, cancel := h.Subscribe(room)
	defer cancel()

	start := time.Now()
	for i := 0; i < 1000; i++ {
		h.Publish(room, Event{Name: "delta", Payload: i})
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("1000 publishes took %s; a dead reader stalled the turn", elapsed)
	}
}

func TestConcurrentPublishAndSubscribeIsRaceFree(t *testing.T) {
	h := NewWithBuffer(8)
	room := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				h.Append(room, "x")
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				_, ch, cancel := h.Subscribe(room)
				select {
				case <-ch:
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
}
