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
	// A parked approval is work, not silence: Hermes may send nothing for the
	// whole approvals.timeout while a human decides. Keep the watchdog alive
	// long enough for that decision and the gateway's own timeout.
	approvalIdle := c.approvalIdleTimeout
	if approvalIdle <= 0 {
		approvalIdle = defaultApprovalIdleTimeout
	}
	timer := time.NewTimer(idle)
	defer timer.Stop()
	resetTimer := func(d time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d)
	}

	var (
		eventName string
		data      []string
		parked    bool
	)

	dispatch := func() error {
		if eventName == "" && len(data) == 0 {
			return nil
		}
		raw := []byte(strings.Join(data, "\n"))
		ev := normalize(eventName, raw, c.logger)
		eventName = ""
		data = data[:0]
		if ev.Kind == "approval" {
			parked = true
		}
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
				if parked {
					resetTimer(approvalIdle)
					parked = false
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue // SSE comment / keepalive is not work; it must not feed the watchdog
			}
			switch {
			case strings.HasPrefix(line, "event:"):
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				resetTimer(idle)
			case strings.HasPrefix(line, "data:"):
				value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
				data = append(data, value)
				resetTimer(idle)
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
		SessionID   string          `json:"session_id"`
		RunID       string          `json:"run_id"`
		MessageID   string          `json:"message_id"`
		Delta       string          `json:"delta"`
		Content     string          `json:"content"`
		ToolName    string          `json:"tool_name"`
		Name        string          `json:"name"`
		Tool        string          `json:"tool"`
		Status      string          `json:"status"`
		RequestID   string          `json:"request_id"`
		Command     string          `json:"command"`
		Description string          `json:"description"`
		Choices     []string        `json:"choices"`
		Error       string          `json:"error"`
		Message     string          `json:"message"`
		Detail      string          `json:"detail"`
		TS          float64         `json:"ts"`
		Usage       json.RawMessage `json:"usage"`
		Runtime     *Runtime        `json:"runtime"`
	}
	_ = json.Unmarshal(raw, &p)

	ev.SessionID = p.SessionID
	ev.RunID = p.RunID
	ev.MessageID = p.MessageID
	ev.TS = p.TS
	ev.Usage = parseUsage(p.Usage)
	ev.Runtime = runtimeOf(p.Runtime, ev.Usage)

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
	case name == "approval.request":
		ev.Kind = "approval"
		ev.Name = firstNonEmpty(p.ToolName, p.Name, p.Tool)
		ev.RequestID = p.RequestID
		ev.Command = p.Command
		ev.Description = p.Description
		ev.Choices = append([]string(nil), p.Choices...)
	case name == "run.cancelled" || name == "assistant.cancelled":
		ev.Kind = "cancelled"
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

// parseUsage reads the terminal usage object. An absent key or JSON null means
// "not reported" (nil); an empty object is a reported all-zero turn and stays
// non-nil, because zero can be a real answer.
func parseUsage(raw json.RawMessage) *Usage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var body struct {
		InputTokens      *json.Number `json:"input_tokens"`
		OutputTokens     *json.Number `json:"output_tokens"`
		TotalTokens      *json.Number `json:"total_tokens"`
		CacheReadTokens  *json.Number `json:"cache_read_tokens"`
		CacheWriteTokens *json.Number `json:"cache_write_tokens"`
		Runtime          *Runtime     `json:"runtime"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	usage := &Usage{
		InputTokens:      numberValue(body.InputTokens),
		OutputTokens:     numberValue(body.OutputTokens),
		TotalTokens:      numberValue(body.TotalTokens),
		CacheReadTokens:  numberValue(body.CacheReadTokens),
		CacheWriteTokens: numberValue(body.CacheWriteTokens),
		CacheReported:    body.CacheReadTokens != nil || body.CacheWriteTokens != nil,
	}
	// The runtime can ride inside usage on some builds; the caller falls back
	// to it when the top-level object is empty.
	if body.Runtime != nil {
		usage.runtime = *body.Runtime
	}
	return usage
}

// runtimeOf prefers the terminal event's top-level runtime, then the copy some
// builds nest inside usage. Empty values stay empty rather than guessed.
func runtimeOf(top *Runtime, usage *Usage) Runtime {
	if top != nil && (top.Provider != "" || top.Model != "") {
		return Runtime{Provider: top.Provider, Model: top.Model}
	}
	if usage != nil {
		return usage.runtime
	}
	return Runtime{}
}

func numberValue(value *json.Number) int64 {
	if value == nil {
		return 0
	}
	n, err := value.Int64()
	if err == nil {
		return n
	}
	f, err := value.Float64()
	if err != nil {
		return 0
	}
	return int64(f)
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
