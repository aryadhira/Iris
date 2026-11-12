package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	irisvoice "github.com/aryadhira/iris/internal/voice"
)

const maxVoiceBytes = 25 << 20

// VoiceClient is the loopback sidecar surface irisd needs. Tests replace it.
type VoiceClient interface {
	Transcribe(ctx context.Context, audio io.Reader, contentType string) (irisvoice.Transcription, error)
	Speak(ctx context.Context, text string) (*irisvoice.Speech, error)
	SpeakStream(ctx context.Context, text string) (io.ReadCloser, error)
}

func voiceUnavailable() map[string]string {
	return errorBody("suara sedang tidak tersedia — coba lagi sebentar.")
}

// voiceTranscribe accepts multipart audio from the browser and relays the raw
// bytes to the sidecar. An empty recording is a normal no-speech answer.
func (s *Server) voiceTranscribe(c echo.Context) error {
	if s.voice == nil {
		return c.JSON(http.StatusServiceUnavailable, voiceUnavailable())
	}

	audio, contentType, err := readVoiceAudio(c, maxVoiceBytes)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Rekaman itu tidak bisa dibaca. Coba lagi."))
	}
	if len(audio) == 0 {
		return c.JSON(http.StatusOK, map[string]any{"text": "", "reason": "no_speech"})
	}

	result, err := s.voice.Transcribe(c.Request().Context(), bytes.NewReader(audio), contentType)
	if err != nil {
		s.logger.Warn("voice transcribe failed", "error", err)
		return c.JSON(http.StatusServiceUnavailable, voiceUnavailable())
	}
	if result.Text == "" {
		return c.JSON(http.StatusOK, map[string]any{
			"text":   "",
			"reason": orNoSpeech(result.Reason),
			"ms":     result.MS,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"text": result.Text,
		"ms":   result.MS,
	})
}

// voiceSpeak streams audio/mpeg back to the browser and names the exact
// sentence the engine received in X-Iris-Spoken-Text.
func (s *Server) voiceSpeak(c echo.Context) error {
	if s.voice == nil {
		return c.JSON(http.StatusServiceUnavailable, voiceUnavailable())
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Teks untuk dibacakan tidak terbaca."))
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return c.JSON(http.StatusBadRequest, errorBody("Tidak ada teks yang bisa dibacakan."))
	}

	speech, err := s.voice.Speak(c.Request().Context(), text)
	if err != nil {
		s.logger.Warn("voice speak failed", "error", err)
		return c.JSON(http.StatusServiceUnavailable, voiceUnavailable())
	}
	defer speech.Audio.Close()

	response := c.Response()
	response.Header().Set(echo.HeaderContentType, "audio/mpeg")
	response.Header().Set("Cache-Control", "no-store")
	if speech.SpokenText != "" {
		response.Header().Set("X-Iris-Spoken-Text", speech.SpokenText)
	}
	if speech.SynthMS != "" {
		response.Header().Set("X-Iris-Synth-Ms", speech.SynthMS)
	}
	response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(response, speech.Audio); err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Warn("voice audio stream ended early", "error", err)
	}
	return nil
}

// voiceSpeakStream proxies the sidecar's newline-delimited JSON without
// buffering. Every line is flushed as soon as it arrives, so the browser gets
// sentence 1's audio while sentence 2 is still being synthesised.
func (s *Server) voiceSpeakStream(c echo.Context) error {
	if s.voice == nil {
		return c.JSON(http.StatusServiceUnavailable, voiceUnavailable())
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Teks untuk dibacakan tidak terbaca."))
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return c.JSON(http.StatusBadRequest, errorBody("Tidak ada teks yang bisa dibacakan."))
	}

	body, err := s.voice.SpeakStream(c.Request().Context(), text)
	if err != nil {
		s.logger.Warn("voice speak stream failed", "error", err)
		return c.JSON(http.StatusServiceUnavailable, voiceUnavailable())
	}
	defer body.Close()

	response := c.Response()
	response.Header().Set(echo.HeaderContentType, "application/x-ndjson")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	response.Flush()

	reader := bufio.NewReaderSize(body, 64*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := response.Write(line); writeErr != nil {
				// Barge-in: the browser went away. Not an error worth a log line
				// at warn level, and never a retry (that would duplicate audio).
				return nil
			}
			response.Flush()
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, context.Canceled) {
				s.logger.Warn("voice stream ended early", "error", readErr)
			}
			return nil
		}
	}
}

func orNoSpeech(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "no_speech"
	}
	return reason
}

// readVoiceAudio accepts the browser's multipart form (field "audio", with
// "file" as a fallback) and also a raw body for curl. Content type is only a
// hint; the sidecar sniffs the bytes.
func readVoiceAudio(c echo.Context, limit int64) ([]byte, string, error) {
	request := c.Request()
	request.Body = http.MaxBytesReader(c.Response(), request.Body, limit)

	mediaType := request.Header.Get(echo.HeaderContentType)
	if strings.HasPrefix(mediaType, "multipart/form-data") {
		if err := request.ParseMultipartForm(limit); err != nil {
			return nil, "", err
		}
		file, header, err := firstVoicePart(request.MultipartForm)
		if err != nil {
			return nil, "", err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, limit))
		if err != nil {
			return nil, "", err
		}
		partType := ""
		if header != nil {
			partType = header.Header.Get(echo.HeaderContentType)
		}
		return data, partType, nil
	}

	data, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", errors.New("recording too large")
	}
	return data, mediaType, nil
}

func firstVoicePart(form *multipart.Form) (multipart.File, *multipart.FileHeader, error) {
	if form == nil {
		return nil, nil, errors.New("empty form")
	}
	for _, field := range []string{"audio", "file"} {
		files := form.File[field]
		if len(files) == 0 {
			continue
		}
		file, err := files[0].Open()
		return file, files[0], err
	}
	return nil, nil, errors.New("no audio part")
}
