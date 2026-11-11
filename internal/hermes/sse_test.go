package hermes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStreamChatParsesNamedEventsAndUnknownKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test server is not a flusher")
		}
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "event: run.started\ndata: {\"session_id\":\"api_1\",\"run_id\":\"run_1\"}\n\n")
		fmt.Fprint(w, "event: assistant.delta\ndata: {\"delta\":\"one\",\"message_id\":\"msg_1\"}\n\n")
		fmt.Fprint(w, "event: future.event\ndata: {\"a\":1,\ndata:  \"b\":2}\n\n")
		fmt.Fprint(w, "event: tool.progress\ndata: {\"tool_name\":\"terminal\",\"status\":\"running\"}\n\n")
		fmt.Fprint(w, "event: assistant.completed\ndata: {\"message_id\":\"msg_1\",\"content\":\"one\"}\n\n")
		fmt.Fprint(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := New(srv.URL, "test-key", testLogger())
	var got []Event
	err := client.StreamChat(context.Background(), "api_1", "hi", func(ev Event) error {
		got = append(got, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("events = %d, want 6: %+v", len(got), got)
	}
	if got[0].Kind != "started" || got[0].RunID != "run_1" {
		t.Errorf("first event = %+v", got[0])
	}
	if got[1].Kind != "delta" || got[1].Text != "one" || got[1].MessageID != "msg_1" {
		t.Errorf("delta event = %+v", got[1])
	}
	if got[2].Kind != "unknown" || got[2].OriginalKind != "future.event" {
		t.Errorf("unknown event = %+v", got[2])
	}
	// Multi-line data was joined with a newline and kept intact.
	if string(got[2].Raw) != "{\"a\":1,\n \"b\":2}" {
		t.Errorf("unknown raw = %q", got[2].Raw)
	}
	if got[3].Kind != "tool" || got[3].Name != "terminal" || got[3].Status != "running" {
		t.Errorf("tool event = %+v", got[3])
	}
	if got[4].Kind != "done" {
		t.Errorf("completed event = %+v", got[4])
	}
	if got[5].Kind != "done" {
		t.Errorf("done event = %+v", got[5])
	}
}

func TestStreamChatIdleWatchdogFailsHonestly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: assistant.delta\ndata: {\"delta\":\"one\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := New(srv.URL, "test-key", testLogger())
	client.SetIdleTimeout(150 * time.Millisecond)

	start := time.Now()
	err := client.StreamChat(context.Background(), "api_1", "hi", func(Event) error { return nil })
	if !errors.Is(err, ErrIdle) {
		t.Fatalf("error = %v, want ErrIdle", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("idle watchdog fired after %s", elapsed)
	}
}

func TestStreamChatIdleWatchdogIsNotFedByKeepalives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "test-key", testLogger())
	client.SetIdleTimeout(150 * time.Millisecond)

	err := client.StreamChat(context.Background(), "api_1", "hi", func(Event) error { return nil })
	if !errors.Is(err, ErrIdle) {
		t.Fatalf("error = %v, want ErrIdle even while keepalives arrive", err)
	}
}

func TestStreamChatHTTPErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()

	client := New(srv.URL, "test-key", testLogger())
	err := client.StreamChat(context.Background(), "api_1", "hi", func(Event) error { return nil })
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("error = %v, want APIError 502", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatal("error leaked the API key")
	}
}
