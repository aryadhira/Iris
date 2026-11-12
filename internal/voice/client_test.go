package voice

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTranscribeParsesNoSpeech(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transcribe" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"","ms":0,"reason":"no_speech"}`))
	}))
	defer server.Close()

	client := New(server.URL, quietLogger())
	got, err := client.Transcribe(context.Background(), strings.NewReader(""), "audio/webm")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got.Text != "" || got.Reason != "no_speech" {
		t.Fatalf("result = %+v", got)
	}
}

func TestSpeakKeepsTheSpokenTextHeader(t *testing.T) {
	audio := strings.Repeat("mp3", 1200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/speak" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"text":"halo"}` {
			t.Fatalf("request body = %s", body)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("X-Iris-Spoken-Text", "halo.")
		_, _ = w.Write([]byte(audio))
	}))
	defer server.Close()

	client := New(server.URL, quietLogger())
	speech, err := client.Speak(context.Background(), "halo")
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	defer speech.Audio.Close()
	if speech.SpokenText != "halo." {
		t.Fatalf("spoken = %q", speech.SpokenText)
	}
	data, _ := io.ReadAll(speech.Audio)
	if string(data) != audio {
		t.Fatalf("audio length = %d", len(data))
	}
}

func TestSidecarErrorIsAnErrorNotHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := New(server.URL, quietLogger())
	_, err := client.Speak(context.Background(), "halo")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "<html>") {
		t.Fatalf("leaked HTML: %v", err)
	}
}
