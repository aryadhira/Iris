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

	"github.com/google/uuid"
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
}

// DefaultBuffer is enough for a fast reader to survive a burst of deltas. When
// it fills, the reader is gone as far as the turn is concerned.
const DefaultBuffer = 256

type subscriber struct {
	ch chan Event
}

type room struct {
	subs    map[*subscriber]struct{}
	partial string
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
	snapshot := Snapshot{Partial: r.partial}
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
				if len(current.subs) == 0 && current.partial == "" {
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
	for sub := range r.subs {
		select {
		case sub.ch <- ev:
		default:
			delete(r.subs, sub)
			close(sub.ch)
		}
	}
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
}

// Reset clears the partial text at the start of a new turn. It does not emit a
// frame; the snapshot of a reattaching client must not inherit yesterday's text.
func (h *Hub) Reset(id uuid.UUID) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[id]; r != nil {
		r.partial = ""
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
