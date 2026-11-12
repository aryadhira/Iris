package hermes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type sseLine struct {
	text string
	err  error
}

// StreamChat posts text into an existing Hermes session and parses the SSE
// response. The request is not bounded by an overall deadline; the idle
// watchdog fails honestly if Hermes sends nothing for c.idleTimeout.
func (c *APIClient) StreamChat(ctx context.Context, sessionID, text, instructions string, onEvent func(Event) error) error {
	if onEvent == nil {
		onEvent = func(Event) error { return nil }
	}

	body := map[string]string{"message": text}
	if strings.TrimSpace(instructions) != "" {
		body["instructions"] = instructions
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode chat message: %w", err)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	url := c.baseURL + "/api/sessions/" + sessionID + "/chat/stream"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build chat request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: cannot reach Hermes: %v", ErrStream, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return &APIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}

	lines := make(chan sseLine)
	stop := make(chan struct{})
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), maxSSELine)
		for scanner.Scan() {
			line := strings.TrimSuffix(scanner.Text(), "\r")
			select {
			case lines <- sseLine{text: line}:
			case <-stop:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- sseLine{err: err}:
			case <-stop:
			}
		}
	}()
	defer func() {
		close(stop)
		resp.Body.Close()
	}()

	idle := c.idleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(idle)
	}

	var (
		eventName string
		data      []string
	)

	dispatch := func() error {
		if eventName == "" && len(data) == 0 {
			return nil
		}
		raw := []byte(strings.Join(data, "\n"))
		ev := normalize(eventName, raw, c.logger)
		eventName = ""
		data = data[:0]
		return onEvent(ev)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			cancel()
			return fmt.Errorf("%w: no data for %s", ErrIdle, idle)
		case item, ok := <-lines:
			if !ok {
				return dispatch()
			}
			if item.err != nil {
				return fmt.Errorf("%w: read error: %v", ErrStream, item.err)
			}
			line := item.text

			if line == "" {
				if err := dispatch(); err != nil {
					return err
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue // SSE comment / keepalive is not work; it must not feed the watchdog
			}
			switch {
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				resetTimer()
			case strings.HasPrefix(line, "data:"):
				value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
				data = append(data, value)
				resetTimer()
			}
		}
	}
}

// normalize maps a Hermes event to the small vocabulary httpapi forwards.
// Anything unrecognised is passed through as Kind "unknown" with Raw intact.
func normalize(name string, raw []byte, logger *slog.Logger) Event {
	ev := Event{
		OriginalKind: name,
		Raw:          append([]byte(nil), raw...),
	}

	var p struct {
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		MessageID string `json:"message_id"`
		Delta     string `json:"delta"`
		Content   string `json:"content"`
		ToolName  string `json:"tool_name"`
		Name      string `json:"name"`
		Tool      string `json:"tool"`
		Status    string `json:"status"`
		Error     string `json:"error"`
		Message   string `json:"message"`
		Detail    string `json:"detail"`
	}
	_ = json.Unmarshal(raw, &p)

	ev.SessionID = p.SessionID
	ev.RunID = p.RunID
	ev.MessageID = p.MessageID

	switch {
	case name == "assistant.delta":
		ev.Kind = "delta"
		ev.Text = p.Delta
	case name == "run.started" || name == "message.started":
		ev.Kind = "started"
	case name == "assistant.completed" || name == "run.completed" || name == "done":
		ev.Kind = "done"
		ev.Text = p.Content
	case name == "tool" || strings.HasPrefix(name, "tool."):
		ev.Kind = "tool"
		ev.Name = firstNonEmpty(p.ToolName, p.Name, p.Tool)
		ev.Status = firstNonEmpty(p.Status, toolStatus(name))
	case name == "run.failed" || name == "assistant.failed" || name == "error" || name == "turn.failed":
		ev.Kind = "error"
		ev.Text = firstNonEmpty(p.Error, p.Message, p.Detail)
	default:
		ev.Kind = "unknown"
		if logger != nil {
			logger.Warn("unknown hermes event", "kind", name, "payload_bytes", len(raw))
		}
	}

	return ev
}

func toolStatus(name string) string {
	switch {
	case name == "tool.progress":
		return "running"
	case strings.HasSuffix(name, ".started"):
		return "started"
	case strings.HasSuffix(name, ".completed"):
		return "completed"
	case strings.HasSuffix(name, ".failed") || strings.HasSuffix(name, ".error"):
		return "failed"
	default:
		return "running"
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
