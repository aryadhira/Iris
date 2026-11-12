package webpush

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/store"
)

type fakeSubs struct {
	mu   sync.Mutex
	rows []store.PushSubscription
}

func (f *fakeSubs) ListPushSubscriptions(context.Context) ([]store.PushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.PushSubscription(nil), f.rows...), nil
}

func (f *fakeSubs) DeletePushSubscription(_ context.Context, endpoint string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.rows[:0]
	var removed int64
	for _, row := range f.rows {
		if row.Endpoint == endpoint {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	f.rows = kept
	return removed, nil
}

func browserKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate subscription key: %v", err)
	}
	pub := elliptic.Marshal(elliptic.P256(), priv.X, priv.Y)
	secret := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(pub), base64.RawURLEncoding.EncodeToString(secret)
}

func testKeys(t *testing.T) Keys {
	t.Helper()
	keys, err := LoadOrGenerate(filepath.Join(t.TempDir(), "vapid.json"), DefaultSubject)
	if err != nil {
		t.Fatalf("VAPID keys: %v", err)
	}
	return keys
}

func TestPayloadShapeIsPlainAndCapped(t *testing.T) {
	long := "# Judul\n\n**Penting** `kode` [tautan](https://example.com) " + strings.Repeat("kata ", 80)
	payload := NewPayload("Ruang Kerja", long, "room-123", "msg-9")
	if payload.Tag != "room-123" {
		t.Fatalf("tag = %q, want room id", payload.Tag)
	}
	if payload.Title != "Ruang Kerja" || payload.RoomID != "room-123" || payload.MessageID != "msg-9" {
		t.Fatalf("payload = %+v", payload)
	}
	if n := utf8.RuneCountInString(payload.Body); n > 120 {
		t.Fatalf("body is %d runes, want <= 120: %q", n, payload.Body)
	}
	for _, markdown := range []string{"#", "**", "`", "](", "```"} {
		if strings.Contains(payload.Body, markdown) {
			t.Fatalf("body still contains %q: %q", markdown, payload.Body)
		}
	}
	if !strings.Contains(payload.Body, "tautan") {
		t.Fatalf("link text was lost: %q", payload.Body)
	}
}

func TestPlainBodyDropsFencedCode(t *testing.T) {
	got := PlainBody("Hasilnya:\n```go\nfmt.Println(\"rahasia\")\n```\nsudah selesai.", 120)
	if strings.Contains(got, "rahasia") || strings.Contains(got, "```") {
		t.Fatalf("code survived: %q", got)
	}
	if !strings.Contains(got, "Hasilnya") || !strings.Contains(got, "sudah selesai") {
		t.Fatalf("plain text was lost: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("body is not one line: %q", got)
	}
}

func TestLoadOrGenerateIsPrivateAndStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid.json")
	first, err := LoadOrGenerate(path, "boss@example.test")
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	second, err := LoadOrGenerate(path, "boss@example.test")
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if first.PrivateKey != second.PrivateKey || first.PublicKey != second.PublicKey {
		t.Fatal("a second call regenerated the keypair")
	}
	if first.PrivateKey == "" || first.PublicKey == "" {
		t.Fatal("empty VAPID keys")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var keys Keys
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("key file is not JSON: %v", err)
	}
	if keys.Subject != "boss@example.test" {
		t.Fatalf("subject = %q", keys.Subject)
	}
}

func TestNotifySendsARealEncryptedPostAndDebouncesPerRoom(t *testing.T) {
	type captured struct {
		contentEncoding string
		authorization   string
		ttl             string
		body            []byte
	}
	var (
		mu    sync.Mutex
		posts []captured
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		posts = append(posts, captured{
			contentEncoding: r.Header.Get("Content-Encoding"),
			authorization:   r.Header.Get("Authorization"),
			ttl:             r.Header.Get("TTL"),
			body:            body,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	p256dh, auth := browserKeys(t)
	subs := &fakeSubs{rows: []store.PushSubscription{{
		ID: uuid.New(), RoomID: uuid.New(), Endpoint: server.URL + "/push/abc", P256dh: p256dh, Auth: auth,
	}}}
	keys := testKeys(t)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	service := NewService(keys, subs, NewRealSender(keys, 5*time.Second), clock, 30*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))

	roomA := uuid.New()
	roomB := uuid.New()
	payload := NewPayload("Kerja", "halo", roomA.String(), "m1")

	if n, err := service.Notify(context.Background(), roomA, payload, false); err != nil || n != 1 {
		t.Fatalf("first notify = %d, %v", n, err)
	}
	if n, _ := service.Notify(context.Background(), roomA, payload, false); n != 0 {
		t.Fatalf("debounced notify sent %d, want 0", n)
	}
	if n, _ := service.Notify(context.Background(), roomB, NewPayload("Lain", "halo", roomB.String(), ""), false); n != 1 {
		t.Fatalf("different room notify = %d, want 1", n)
	}
	if n, _ := service.Notify(context.Background(), roomA, payload, true); n != 1 {
		t.Fatalf("approval notify = %d, want 1 (exempt from debounce)", n)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(posts) != 3 {
		t.Fatalf("POSTs = %d, want 3", len(posts))
	}
	for i, post := range posts {
		if post.contentEncoding != "aes128gcm" {
			t.Errorf("post %d encoding = %q", i, post.contentEncoding)
		}
		if !strings.HasPrefix(post.authorization, "vapid t=") || !strings.Contains(post.authorization, "k="+keys.PublicKey) {
			t.Errorf("post %d VAPID header = %q", i, post.authorization)
		}
		if post.ttl != "300" {
			t.Errorf("post %d TTL = %q, want 300", i, post.ttl)
		}
		if len(post.body) < 86+16 {
			t.Fatalf("post %d body is too short to be aes128gcm: %d bytes", i, len(post.body))
		}
		if post.body[20] != 65 {
			t.Errorf("post %d key id length = %d, want 65", i, post.body[20])
		}
	}
}
