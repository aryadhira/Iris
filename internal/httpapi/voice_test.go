package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/auth"
	irisvoice "github.com/aryadhira/iris/internal/voice"
)

type fakeVoice struct {
	transcribe  func(ctx context.Context, audio io.Reader, contentType string) (irisvoice.Transcription, error)
	speak       func(ctx context.Context, text string) (*irisvoice.Speech, error)
	speakStream func(ctx context.Context, text string) (io.ReadCloser, error)

	transcribeCalls int
	speakText       string
	streamText      string
}

func (f *fakeVoice) Transcribe(ctx context.Context, audio io.Reader, contentType string) (irisvoice.Transcription, error) {
	f.transcribeCalls++
	if f.transcribe != nil {
		return f.transcribe(ctx, audio, contentType)
	}
	return irisvoice.Transcription{}, nil
}

func (f *fakeVoice) Speak(ctx context.Context, text string) (*irisvoice.Speech, error) {
	f.speakText = text
	if f.speak != nil {
		return f.speak(ctx, text)
	}
	return &irisvoice.Speech{Audio: io.NopCloser(strings.NewReader(""))}, nil
}

func (f *fakeVoice) SpeakStream(ctx context.Context, text string) (io.ReadCloser, error) {
	f.streamText = text
	if f.speakStream != nil {
		return f.speakStream(ctx, text)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func newVoiceTestApp(t *testing.T, v VoiceClient) *echo.Echo {
	t.Helper()
	st := newFakeStore()
	logger := testLogger()
	return New(Options{
		Store:   st,
		Hermes:  &fakeHermes{},
		Voice:   v,
		Auth:    auth.New(st, testPassphrase, false, logger),
		Logger:  logger,
		Version: "test",
		DataDir: t.TempDir(),
	})
}

func multipartAudio(t *testing.T, field, filename, contentType string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return body, writer.FormDataContentType()
}

func voicePost(t *testing.T, e *echo.Echo, path string, body io.Reader, contentType string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set(echo.HeaderContentType, contentType)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestVoiceTranscribeRelaysTheRecording(t *testing.T) {
	fv := &fakeVoice{
		transcribe: func(_ context.Context, audio io.Reader, contentType string) (irisvoice.Transcription, error) {
			data, _ := io.ReadAll(audio)
			if string(data) != "fake-webm-bytes" {
				t.Fatalf("sidecar got %q", data)
			}
			if !strings.HasPrefix(contentType, "audio/") {
				t.Fatalf("content-type relayed = %q", contentType)
			}
			return irisvoice.Transcription{Text: "ingatkan saya besok", MS: 812}, nil
		},
	}
	e := newVoiceTestApp(t, fv)
	cookie := login(t, e)
	body, contentType := multipartAudio(t, "audio", "clip.webm", "audio/webm", []byte("fake-webm-bytes"))

	rec := voicePost(t, e, "/api/voice/transcribe", body, contentType, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Text string  `json:"text"`
		MS   float64 `json:"ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Text != "ingatkan saya besok" {
		t.Fatalf("text = %q", out.Text)
	}
}

func TestVoiceTranscribeEmptyIsNoSpeech(t *testing.T) {
	fv := &fakeVoice{}
	e := newVoiceTestApp(t, fv)
	cookie := login(t, e)
	body, contentType := multipartAudio(t, "audio", "clip.webm", "audio/webm", nil)

	rec := voicePost(t, e, "/api/voice/transcribe", body, contentType, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["reason"] != "no_speech" || out["text"] != "" {
		t.Fatalf("body = %v, want empty no_speech", out)
	}
	if fv.transcribeCalls != 0 {
		t.Fatalf("empty recording reached the sidecar %d times", fv.transcribeCalls)
	}
}

func TestVoiceSpeakStreamsAudioAndSpokenText(t *testing.T) {
	audio := bytes.Repeat([]byte{0x49, 0x44, 0x33}, 900) // >2 KB of MP3-ish bytes
	fv := &fakeVoice{
		speak: func(_ context.Context, text string) (*irisvoice.Speech, error) {
			if text != "**Selesai**" {
				t.Fatalf("text to sidecar = %q", text)
			}
			return &irisvoice.Speech{
				Audio:      io.NopCloser(bytes.NewReader(audio)),
				SpokenText: "Selesai.",
				SynthMS:    "640",
			}, nil
		},
	}
	e := newVoiceTestApp(t, fv)
	cookie := login(t, e)

	rec := voicePost(t, e, "/api/voice/speak", strings.NewReader(`{"text":"**Selesai**"}`), echo.MIMEApplicationJSON, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(echo.HeaderContentType); got != "audio/mpeg" {
		t.Fatalf("content-type = %q", got)
	}
	if got := rec.Header().Get("X-Iris-Spoken-Text"); got != "Selesai." {
		t.Fatalf("spoken header = %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), audio) {
		t.Fatalf("audio body = %d bytes, want %d", rec.Body.Len(), len(audio))
	}
	if fv.speakText != "**Selesai**" {
		t.Fatalf("handler changed the text before the sidecar: %q", fv.speakText)
	}
}

func TestVoiceSidecarDownIs503InIndonesian(t *testing.T) {
	fv := &fakeVoice{
		transcribe: func(context.Context, io.Reader, string) (irisvoice.Transcription, error) {
			return irisvoice.Transcription{}, errors.New("connection refused")
		},
		speak: func(context.Context, string) (*irisvoice.Speech, error) {
			return nil, errors.New("connection refused")
		},
	}
	e := newVoiceTestApp(t, fv)
	cookie := login(t, e)
	body, contentType := multipartAudio(t, "audio", "clip.webm", "audio/webm", []byte("fake-webm-bytes"))

	rec := voicePost(t, e, "/api/voice/transcribe", body, contentType, cookie)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("transcribe status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "suara sedang tidak tersedia") {
		t.Fatalf("copy is not Indonesian: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "goroutine") || strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatalf("leaked internals: %s", rec.Body.String())
	}

	rec = voicePost(t, e, "/api/voice/speak", strings.NewReader(`{"text":"halo"}`), echo.MIMEApplicationJSON, cookie)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("speak status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "suara sedang tidak tersedia") {
		t.Fatalf("speak copy is not Indonesian: %s", rec.Body.String())
	}
}

func TestVoiceEndpointsRequireAuth(t *testing.T) {
	e := newVoiceTestApp(t, &fakeVoice{})
	body, contentType := multipartAudio(t, "audio", "clip.webm", "audio/webm", []byte("x"))
	rec := voicePost(t, e, "/api/voice/transcribe", body, contentType, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
