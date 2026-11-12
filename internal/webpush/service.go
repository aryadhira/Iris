// Package webpush sends standards-compliant Web Push notifications (aes128gcm
// + VAPID). The private key never leaves the key file and is never logged.
package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	wp "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/store"
)

const (
	DefaultSubject  = "iris@localhost"
	DefaultDebounce = 30 * time.Second
	maxBodyRunes    = 120
)

// Keys is the persisted VAPID material. JSON on disk, mode 0600.
type Keys struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	Subject    string `json:"subject"`
}

// Payload is the exact JSON the service worker receives.
type Payload struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	RoomID    string `json:"roomId"`
	MessageID string `json:"messageId,omitempty"`
	Tag       string `json:"tag"`
}

// SubscriptionStore is the persistent side of a push subscription.
type SubscriptionStore interface {
	ListPushSubscriptions(ctx context.Context) ([]store.PushSubscription, error)
	DeletePushSubscription(ctx context.Context, endpoint string) (int64, error)
}

// Sender is the wire seam: tests use a real encrypted POST to httptest.
type Sender interface {
	Send(ctx context.Context, sub store.PushSubscription, payload Payload) error
}

// SendError keeps the HTTP status so a 404/410 subscription can be pruned.
type SendError struct {
	Status int
	Body   string
}

func (e *SendError) Error() string {
	return fmt.Sprintf("push endpoint answered %d", e.Status)
}

// Service owns debouncing and fan-out. A missing key is the only reason it is
// absent; a missing subscription is simply a no-op.
type Service struct {
	keys     Keys
	subs     SubscriptionStore
	sender   Sender
	now      func() time.Time
	debounce time.Duration
	logger   *slog.Logger

	mu   sync.Mutex
	last map[uuid.UUID]time.Time
}

// New loads (or generates once) the VAPID keypair, then builds the real sender.
func New(keyFile, subject string, subs SubscriptionStore, logger *slog.Logger) (*Service, error) {
	keys, err := LoadOrGenerate(keyFile, subject)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return NewService(keys, subs, NewRealSender(keys, 10*time.Second), time.Now, DefaultDebounce, logger), nil
}

// NewService is the testable constructor.
func NewService(keys Keys, subs SubscriptionStore, sender Sender, now func() time.Time, debounce time.Duration, logger *slog.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		keys:     keys,
		subs:     subs,
		sender:   sender,
		now:      now,
		debounce: debounce,
		logger:   logger,
		last:     make(map[uuid.UUID]time.Time),
	}
}

// PublicKey is the only VAPID value the browser is ever given.
func (s *Service) PublicKey() string {
	if s == nil {
		return ""
	}
	return s.keys.PublicKey
}

// Notify sends one payload to every installed device. Debounce is per room;
// an approval is exempt because it blocks the run.
func (s *Service) Notify(ctx context.Context, roomID uuid.UUID, payload Payload, exempt bool) (int, error) {
	if s == nil || s.sender == nil {
		return 0, nil
	}
	now := s.now()
	if !exempt {
		s.mu.Lock()
		if last, ok := s.last[roomID]; ok && now.Sub(last) < s.debounce {
			s.mu.Unlock()
			return 0, nil
		}
		s.last[roomID] = now
		s.mu.Unlock()
	}

	subs, err := s.subs.ListPushSubscriptions(ctx)
	if err != nil {
		return 0, err
	}
	if len(subs) == 0 {
		return 0, nil
	}

	var failures []error
	sent := 0
	for _, sub := range subs {
		if err := s.sender.Send(ctx, sub, payload); err != nil {
			var sendErr *SendError
			if errors.As(err, &sendErr) && (sendErr.Status == http.StatusNotFound || sendErr.Status == http.StatusGone) {
				if _, delErr := s.subs.DeletePushSubscription(ctx, sub.Endpoint); delErr != nil {
					s.logger.Warn("cannot remove stale push subscription", "error", delErr, "subscription_id", sub.ID.String())
				}
			}
			s.logger.Warn("push not delivered", "error", err, "room_id", roomID.String(), "subscription_id", sub.ID.String())
			failures = append(failures, err)
			continue
		}
		sent++
	}
	return sent, errors.Join(failures...)
}

// NewPayload builds the service-worker shape. The body is plain text, capped,
// and the tag is the room id so one room never stacks six notifications.
func NewPayload(roomName, text, roomID, messageID string) Payload {
	title := strings.TrimSpace(roomName)
	if title == "" {
		title = "Iris"
	}
	return Payload{
		Title:     title,
		Body:      PlainBody(text, maxBodyRunes),
		RoomID:    roomID,
		MessageID: messageID,
		Tag:       roomID,
	}
}

var (
	fenceRE   = regexp.MustCompile("(?s)```.*?```")
	inlineRE  = regexp.MustCompile("`([^`]*)`")
	linkRE    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	headingRE = regexp.MustCompile(`(?m)^\s*#{1,6}\s*`)
	bulletRE  = regexp.MustCompile(`(?m)^\s*[-*+]\s+`)
)

// PlainBody strips the noisy markdown and truncates on a rune boundary.
func PlainBody(text string, max int) string {
	if max <= 0 {
		max = maxBodyRunes
	}
	out := fenceRE.ReplaceAllString(text, " ")
	out = inlineRE.ReplaceAllString(out, "$1")
	out = linkRE.ReplaceAllString(out, "$1")
	out = headingRE.ReplaceAllString(out, "")
	out = bulletRE.ReplaceAllString(out, "")
	out = strings.NewReplacer("**", "", "__", "", "~~", "", "*", "").Replace(out)
	out = strings.Join(strings.Fields(out), " ")
	if out == "" {
		return "Ada kabar baru dari Iris."
	}
	if utf8.RuneCountInString(out) <= max {
		return out
	}
	runes := []rune(out)
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

// RealSender implements RFC 8291/8292 through the pinned webpush-go library.
type RealSender struct {
	keys   Keys
	client *http.Client
}

func NewRealSender(keys Keys, timeout time.Duration) *RealSender {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &RealSender{keys: keys, client: &http.Client{Timeout: timeout}}
}

func (r *RealSender) Send(ctx context.Context, sub store.PushSubscription, payload Payload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := wp.SendNotificationWithContext(ctx, body, &wp.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     wp.Keys{Auth: sub.Auth, P256dh: sub.P256dh},
	}, &wp.Options{
		HTTPClient:      r.client,
		Subscriber:      r.keys.Subject,
		TTL:             300,
		Urgency:         wp.UrgencyNormal,
		VAPIDPublicKey:  r.keys.PublicKey,
		VAPIDPrivateKey: r.keys.PrivateKey,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &SendError{Status: resp.StatusCode, Body: strings.TrimSpace(string(preview))}
	}
	return nil
}

// LoadOrGenerate reads the keypair once, or creates it atomically with 0600.
// It never returns the private key in an error or a log line.
func LoadOrGenerate(path, subject string) (Keys, error) {
	if strings.TrimSpace(path) == "" {
		return Keys{}, errors.New("VAPID key file path is empty")
	}
	if strings.TrimSpace(subject) == "" {
		subject = DefaultSubject
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		var keys Keys
		if err := json.Unmarshal(raw, &keys); err != nil {
			return Keys{}, fmt.Errorf("VAPID key file at %s is not valid JSON", path)
		}
		if keys.PrivateKey == "" || keys.PublicKey == "" {
			return Keys{}, fmt.Errorf("VAPID key file at %s is incomplete", path)
		}
		if keys.Subject == "" {
			keys.Subject = subject
		}
		_ = os.Chmod(path, 0o600)
		return keys, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Keys{}, fmt.Errorf("cannot read VAPID key file at %s: %v", path, err)
	}

	privateKey, publicKey, err := wp.GenerateVAPIDKeys()
	if err != nil {
		return Keys{}, fmt.Errorf("cannot generate VAPID keys: %w", err)
	}
	keys := Keys{PrivateKey: privateKey, PublicKey: publicKey, Subject: subject}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Keys{}, fmt.Errorf("cannot create VAPID key directory %s: %w", dir, err)
	}
	raw, err = json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return Keys{}, err
	}
	tmp, err := os.CreateTemp(dir, ".vapid-*.json")
	if err != nil {
		return Keys{}, fmt.Errorf("cannot create VAPID key file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return Keys{}, err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return Keys{}, err
	}
	if err := tmp.Close(); err != nil {
		return Keys{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return Keys{}, fmt.Errorf("cannot install VAPID key file at %s: %w", path, err)
	}
	return keys, nil
}
