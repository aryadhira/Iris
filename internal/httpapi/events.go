package httpapi

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// roomEvent is one typed SSE frame that belongs to a room, not to a single
// HTTP response. An approval is the first producer: the turn stream that
// created it may be gone while another tab is still open.
type roomEvent struct {
	Name    string
	Payload any
	ack     chan struct{}
}

// roomHub is a tiny in-process fan-out. Subscribers are turn streams; a
// fixture or an approval resolution can publish without owning a stream.
type roomHub struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan roomEvent]struct{}
}

func newRoomHub() *roomHub {
	return &roomHub{subs: make(map[uuid.UUID]map[chan roomEvent]struct{})}
}

func (h *roomHub) subscribe(roomID uuid.UUID) (<-chan roomEvent, func()) {
	ch := make(chan roomEvent, 64)
	h.mu.Lock()
	if h.subs[roomID] == nil {
		h.subs[roomID] = make(map[chan roomEvent]struct{})
	}
	h.subs[roomID][ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if set, ok := h.subs[roomID]; ok {
				delete(set, ch)
				if len(set) == 0 {
					delete(h.subs, roomID)
				}
			}
			h.mu.Unlock()
		})
	}
}

// count reports how many SSE clients are attached to a room right now. It is
// what decides whether a finished run needs a push: a phone that is already
// watching does not need a notification.
func (h *roomHub) count(roomID uuid.UUID) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[roomID])
}

// publish never blocks a turn. A slow tab loses the frame rather than stalling
// Hermes; the approval is still in the database and the room reloads it.
func (h *roomHub) publish(roomID uuid.UUID, ev roomEvent) {
	h.mu.Lock()
	targets := make([]chan roomEvent, 0, len(h.subs[roomID]))
	for ch := range h.subs[roomID] {
		targets = append(targets, ch)
	}
	h.mu.Unlock()

	for _, ch := range targets {
		frame := ev
		frame.ack = make(chan struct{})
		delivered := false
		select {
		case ch <- frame:
			delivered = true
		default:
			// Drop the oldest queued frame to make room for the newest state.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- frame:
				delivered = true
			default:
			}
		}
		if delivered {
			// The current turn's stream gets the frame before the turn continues;
			// another tab that never reads cannot stall Hermes for more than 1 s.
			select {
			case <-frame.ack:
			case <-time.After(time.Second):
			}
		}
	}
}
