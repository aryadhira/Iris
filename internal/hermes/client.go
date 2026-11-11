// Package hermes is a small typed client for the Hermes api_server gateway.
package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// Event is a normalized Hermes SSE event. Unknown event kinds are preserved
// with Kind == "unknown" and the original payload in Raw; they never abort a
// turn.
type Event struct {
	Kind         string // started, delta, tool, done, error, unknown
	OriginalKind string
	SessionID    string
	RunID        string
	MessageID    string
	Text         string
	Name         string
	Status       string
	Raw          []byte
}

// Client is the surface httpapi depends on. Tests use a fake implementation.
type Client interface {
	CreateSession(ctx context.Context, title string) (id string, err error)
	StreamChat(ctx context.Context, sessionID, text string, onEvent func(Event) error) error
	DeleteSession(ctx context.Context, sessionID string) error
}

var (
	// ErrIdle is returned when Hermes accepts the stream but sends nothing for
	// the idle window.
	ErrIdle = errors.New("hermes stream went idle")
	// ErrStream is returned when a stream ends without a usable response.
	ErrStream = errors.New("hermes stream failed")
)

// APIError describes a non-2xx answer from Hermes.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hermes returned HTTP %d: %s", e.StatusCode, e.Body)
}

const (
	defaultIdleTimeout = 90 * time.Second
	connectTimeout     = 10 * time.Second
	maxErrorBody       = 4 << 10
	maxSSELine         = 4 << 20
)

// APIClient talks to the Hermes api_server over loopback with bearer auth.
type APIClient struct {
	baseURL     string
	apiKey      string
	http        *http.Client
	logger      *slog.Logger
	idleTimeout time.Duration
}

// New returns a client for baseURL (for example http://127.0.0.1:8642).
func New(baseURL, apiKey string, logger *slog.Logger) *APIClient {
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{
		Proxy:                 nil, // loopback only; never route the key through a proxy
		DialContext:           (&net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   connectTimeout,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          10,
	}
	return &APIClient{
		baseURL:     strings.TrimRight(baseURL, "/"),
		apiKey:      apiKey,
		http:        &http.Client{Transport: transport},
		logger:      logger,
		idleTimeout: defaultIdleTimeout,
	}
}

// SetIdleTimeout overrides the 90 s idle watchdog (used by tests).
func (c *APIClient) SetIdleTimeout(d time.Duration) {
	if d > 0 {
		c.idleTimeout = d
	}
}

// CreateSession creates a Hermes session and returns its stable id.
func (c *APIClient) CreateSession(ctx context.Context, title string) (string, error) {
	payload, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		return "", fmt.Errorf("encode create session: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/sessions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build create session request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("create hermes session: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return "", fmt.Errorf("read create session response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", &APIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}

	var decoded struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode create session response: %w", err)
	}
	if decoded.Session.ID == "" {
		return "", fmt.Errorf("%w: Hermes returned no session id", ErrStream)
	}
	return decoded.Session.ID, nil
}

// DeleteSession removes a Hermes session. It is used to clean up an orphan when
// a room row cannot be written.
func (c *APIClient) DeleteSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/api/sessions/"+sessionID, nil)
	if err != nil {
		return fmt.Errorf("build delete session request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete hermes session: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{StatusCode: resp.StatusCode, Body: resp.Status}
	}
	return nil
}
