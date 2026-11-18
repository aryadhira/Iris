// Package live is Iris' per-room event fan-out.
//
// A room is a map of subscribers and the partial text of the turn that is
// writing right now. Publishing is deliberately non-blocking: a phone on a bad
// train must never stall the turn that is producing the answer. A subscriber
// whose queue is full is removed, not waited on; it can reattach and get a
// fresh snapshot from the HTTP layer.
package live

import (
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/turntrace"
)

// Event is one typed SSE frame owned by a room.
type Event struct {
	Name    string
	Payload any
}

// Snapshot is the in-memory half of a late joiner's view. The HTTP layer adds
// the persisted run, approvals and reminders.
type Snapshot struct {
	Partial string
	// Steps are the in-flight working log at the instant of subscription. A
	// phone returning from the background sees the same list as the laptop.
	Steps []turntrace.Step
	// LastEventAgeMs is how long the turn had been silent when the snapshot was
	// taken. The panel starts its "menunggu" clock from this, not from zero.
	LastEventAgeMs int64
	// Active is true while a turn owns the room.
	Active bool
}

// DefaultBuffer is enough for a fast reader to survive a burst of deltas. When
// it fills, the reader is gone as far as the turn is concerned.
const DefaultBuffer = 256

type subscriber struct {
	ch chan Event
}

type room struct {
	subs        map[*subscriber]struct{}
	partial     string
	steps       []turntrace.Step
	active      bool
	lastEventAt time.Time
}

// Hub fans events out to every client attached to a room.
type Hub struct {
	mu    sync.Mutex
	rooms map[uuid.UUID]*room
	buf   int
}

// New creates an empty hub.
func New() *Hub {
	return NewWithBuffer(DefaultBuffer)
}

// NewWithBuffer is used by tests to make a slow subscriber fill its queue
// quickly without publishing hundreds of frames.
func NewWithBuffer(buffer int) *Hub {
	if buffer < 1 {
		buffer = 1
	}
	return &Hub{rooms: make(map[uuid.UUID]*room), buf: buffer}
}

func (h *Hub) roomLocked(id uuid.UUID) *room {
	r := h.rooms[id]
	if r == nil {
		r = &room{subs: make(map[*subscriber]struct{})}
		h.rooms[id] = r
	}
	return r
}

// Subscribe registers a reader and captures the partial text at the same
// instant. A delta published after this call is queued for the reader; text
// already streamed is in the snapshot, so the client never sees it twice.
func (h *Hub) Subscribe(id uuid.UUID) (Snapshot, <-chan Event, func()) {
	if h == nil {
		return Snapshot{}, nil, func() {}
	}
	h.mu.Lock()
	r := h.roomLocked(id)
	sub := &subscriber{ch: make(chan Event, h.buf)}
	r.subs[sub] = struct{}{}
	snapshot := Snapshot{
		Partial:        r.partial,
		Steps:          append([]turntrace.Step(nil), r.steps...),
		LastEventAgeMs: ageMs(r),
		Active:         r.active,
	}
	h.mu.Unlock()

	var once sync.Once
	return snapshot, sub.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if current, ok := h.rooms[id]; ok && current == r {
				if _, exists := current.subs[sub]; exists {
					delete(current.subs, sub)
					close(sub.ch)
				}
				if len(current.subs) == 0 && current.partial == "" && len(current.steps) == 0 {
					delete(h.rooms, id)
				}
			}
			h.mu.Unlock()
		})
	}
}

// Publish sends one frame to every current subscriber. A full queue means the
// reader is not keeping up: remove it rather than delaying the turn by even one
// write. The removed reader's stream closes and it can reattach with a snapshot.
func (h *Hub) Publish(id uuid.UUID, ev Event) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[id]
	if r == nil {
		return
	}
	// A heartbeat is not work: counting it would make a stalled turn look fresh.
	if ev.Name != "activity" {
		r.lastEventAt = time.Now()
	}
	for sub := range r.subs {
		select {
		case sub.ch <- ev:
		default:
			delete(r.subs, sub)
			close(sub.ch)
		}
	}
}

// PublishStep stores a step and fans it out. Storing before publishing is what
// makes a snapshot taken a microsecond later include it exactly once.
func (h *Hub) PublishStep(id uuid.UUID, step turntrace.Step) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.roomLocked(id)
	updated := false
	for i := range r.steps {
		if r.steps[i].Seq == step.Seq {
			r.steps[i] = step
			updated = true
			break
		}
	}
	if !updated {
		r.steps = append(r.steps, step)
	}
	r.lastEventAt = time.Now()
	h.publishLocked(r, Event{Name: "step", Payload: step})
}

// BeginTurn starts a fresh working log. It never touches the subscriber list.
func (h *Hub) BeginTurn(id uuid.UUID) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.roomLocked(id)
	r.partial = ""
	r.steps = nil
	r.active = true
	r.lastEventAt = time.Now()
}

// TurnState returns the in-flight steps and the age of the last real event.
// ok is false when no turn owns the room.
func (h *Hub) TurnState(id uuid.UUID) ([]turntrace.Step, time.Duration, bool) {
	if h == nil {
		return nil, 0, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[id]
	if r == nil || !r.active {
		return nil, 0, false
	}
	return append([]turntrace.Step(nil), r.steps...), age(r), true
}

// Activity reports the silence age while a turn is live. It is the honest
// heartbeat: no subscriber, no frame, and no reset unless something happened.
func (h *Hub) Activity(id uuid.UUID) (time.Duration, bool) {
	if h == nil {
		return 0, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[id]
	if r == nil || !r.active {
		return 0, false
	}
	return age(r), true
}

func age(r *room) time.Duration {
	if r == nil || r.lastEventAt.IsZero() {
		return 0
	}
	d := time.Since(r.lastEventAt)
	if d < 0 {
		return 0
	}
	return d
}

func ageMs(r *room) int64 {
	return age(r).Milliseconds()
}

// Append adds visible text to the room's partial reply and publishes it as one
// delta under the same lock that a Subscribe takes. This is what makes the
// snapshot/delta boundary exact.
func (h *Hub) Append(id uuid.UUID, text string) {
	if h == nil || text == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.roomLocked(id)
	r.partial += text
	r.lastEventAt = time.Now()
	h.publishLocked(r, Event{Name: "delta", Payload: map[string]string{"text": text}})
}

func (h *Hub) publishLocked(r *room, ev Event) {
	for sub := range r.subs {
		select {
		case sub.ch <- ev:
		default:
			delete(r.subs, sub)
			close(sub.ch)
		}
	}
}

// SetPartial replaces the partial text without publishing a delta. Used when a
// turn adopts a body that was already persisted.
func (h *Hub) SetPartial(id uuid.UUID, text string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.roomLocked(id)
	r.partial = text
	r.lastEventAt = time.Now()
}

// Reset clears the in-flight view at the end of a turn. It does not emit a
// frame; the stored message (with its steps) is the history a client reads.
func (h *Hub) Reset(id uuid.UUID) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[id]; r != nil {
		r.partial = ""
		r.steps = nil
		r.active = false
		r.lastEventAt = time.Time{}
		if len(r.subs) == 0 {
			delete(h.rooms, id)
		}
	}
}

// Partial returns the text streamed so far, for the SSE snapshot.
func (h *Hub) Partial(id uuid.UUID) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[id]; r != nil {
		return r.partial
	}
	return ""
}

// Count reports how many clients are attached right now. It decides whether a
// finished run needs a Web Push.
func (h *Hub) Count(id uuid.UUID) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[id]; r != nil {
		return len(r.subs)
	}
	return 0
}
