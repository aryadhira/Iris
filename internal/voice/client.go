// Package voice is the loopback client for the Iris voice sidecar (STT + TTS).
// The browser never reaches this service; irisd is the only caller.
package voice

import (
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

// Transcription is the sidecar's answer. An empty Text with Reason "no_speech"
// is a normal event: a quick tap records nothing.
type Transcription struct {
	Text   string  `json:"text"`
	Reason string  `json:"reason,omitempty"`
	MS     float64 `json:"ms,omitempty"`
}

// Speech is a synthesised response. Audio is streamed, not buffered a second
// time in irisd; the caller must close it.
type Speech struct {
	Audio      io.ReadCloser
	SpokenText string
	SynthMS    string
}

// Client speaks the sidecar's small HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
	logger  *slog.Logger
}

// New builds a client. baseURL is loopback by default (http://127.0.0.1:3310).
func New(baseURL string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 120 * time.Second},
		logger:  logger,
	}
}

// Transcribe posts raw audio bytes. The sidecar sniffs the container; the
// contentType is a hint only.
func (c *Client) Transcribe(ctx context.Context, audio io.Reader, contentType string) (Transcription, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/transcribe", audio)
	if err != nil {
		return Transcription{}, err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return Transcription{}, fmt.Errorf("voice sidecar unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Transcription{}, fmt.Errorf("voice sidecar answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out Transcription
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Transcription{}, fmt.Errorf("voice sidecar sent unreadable JSON: %w", err)
	}
	return out, nil
}

// SpeakStream asks for the streaming protocol. The caller owns the body; each
// newline is one JSON object (see voice/README.md).
func (c *Client) SpeakStream(ctx context.Context, text string) (io.ReadCloser, error) {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/speak/stream", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voice sidecar unreachable: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("voice sidecar answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

// Speak asks for speech and returns the audio stream plus the exact sentence
// the engine was given.
func (c *Client) Speak(ctx context.Context, text string) (*Speech, error) {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/speak", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voice sidecar unreachable: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("voice sidecar answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return &Speech{
		Audio:      resp.Body,
		SpokenText: resp.Header.Get("X-Iris-Spoken-Text"),
		SynthMS:    resp.Header.Get("X-Iris-Synth-Ms"),
	}, nil
}
