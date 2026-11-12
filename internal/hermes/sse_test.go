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
	err := client.StreamChat(context.Background(), "api_1", "hi", "", func(ev Event) error {
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

func TestNormalizeReadsTerminalUsageAndServedRuntime(t *testing.T) {
	// Captured from the live gateway on 2026-11-12 (build v0.21.6+636): the
	// session stream reports three counters and nests runtime inside usage.
	captured := []byte(`{"session_id":"api_1794472917_53c0e61d","message_id":"msg_1","completed":true,"partial":false,"interrupted":false,"messages":[{"role":"assistant","content":"Halo, Boss."}],"usage":{"input_tokens":16619,"output_tokens":6,"total_tokens":16625,"runtime":{"provider":"opencode-go","model":"deepseek-v4.1-flash","route_source":"global","requested":{"provider":"","model":""}}},"runtime":{"provider":"opencode-go","model":"deepseek-v4.1-flash","route_source":"global"},"run_id":"run_1","seq":8}`)
	ev := normalize("run.completed", captured, testLogger())
	if ev.Kind != "done" {
		t.Fatalf("kind = %q", ev.Kind)
	}
	if ev.Usage == nil {
		t.Fatal("usage was not parsed")
	}
	if ev.Usage.InputTokens != 16619 || ev.Usage.OutputTokens != 6 || ev.Usage.TotalTokens != 16625 {
		t.Fatalf("usage = %+v", *ev.Usage)
	}
	if ev.Usage.CacheReported {
		t.Fatal("cache_reported = true although this payload names no cache counter")
	}
	if ev.Runtime.Provider != "opencode-go" || ev.Runtime.Model != "deepseek-v4.1-flash" {
		t.Fatalf("runtime = %+v", ev.Runtime)
	}

	// A five-counter payload (the /v1/runs shape) is read in full.
	five := []byte(`{"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":18,"cache_read_tokens":5,"cache_write_tokens":1},"runtime":{"provider":"deepseek","model":"deepseek-v4-flash"}}`)
	ev = normalize("run.completed", five, testLogger())
	if ev.Usage == nil || ev.Usage.CacheReadTokens != 5 || ev.Usage.CacheWriteTokens != 1 {
		t.Fatalf("five-counter usage = %+v", ev.Usage)
	}
	if !ev.Usage.CacheReported {
		t.Fatal("cache_reported = false for a payload that names both cache counters")
	}

	// No usage object at all is "not reported", not zero.
	absent := normalize("run.completed", []byte(`{"run_id":"run_2","runtime":{"provider":"x","model":"y"}}`), testLogger())
	if absent.Usage != nil {
		t.Fatalf("usage = %+v, want nil for an absent payload", absent.Usage)
	}

	// The bare done sentinel must not invent usage.
	sentinel := normalize("done", []byte(`{"run_id":"run_2"}`), testLogger())
	if sentinel.Usage != nil {
		t.Fatalf("sentinel usage = %+v, want nil", sentinel.Usage)
	}
}

func TestNormalizeKeepsNestedRuntimeWhenTopLevelIsEmpty(t *testing.T) {
	raw := []byte(`{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"runtime":{"provider":"p","model":"m"}}}`)
	ev := normalize("run.completed", raw, testLogger())
	if ev.Runtime.Provider != "p" || ev.Runtime.Model != "m" {
		t.Fatalf("nested runtime = %+v", ev.Runtime)
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
	err := client.StreamChat(context.Background(), "api_1", "hi", "", func(Event) error { return nil })
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

	err := client.StreamChat(context.Background(), "api_1", "hi", "", func(Event) error { return nil })
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
	err := client.StreamChat(context.Background(), "api_1", "hi", "", func(Event) error { return nil })
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("error = %v, want APIError 502", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatal("error leaked the API key")
	}
}
