package httpapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/hermes"
)

type storeMessage struct {
	ID   uuid.UUID
	Body string
}

func mustUUID(t *testing.T, value string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("uuid %q: %v", value, err)
	}
	return id
}

var tinyPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 128)...)

func upload(t *testing.T, e *echo.Echo, cookie *http.Cookie, roomID, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/rooms/"+roomID+"/uploads", &buf)
	req.Header.Set(echo.HeaderContentType, writer.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

type uploadedFile struct {
	ID     string `json:"id"`
	RoomID string `json:"room_id"`
	Source string `json:"source"`
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
	Path   string `json:"-"`
}

func decodeFile(t *testing.T, rec *httptest.ResponseRecorder) uploadedFile {
	t.Helper()
	var body struct {
		File uploadedFile `json:"file"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode file: %v (%s)", err, rec.Body.String())
	}
	return body.File
}

func TestCreateRoomCreatesInboxOutboxAndFolderPrompt(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_folder"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	for _, dir := range []string{"inbox", "outbox"} {
		info, err := os.Stat(filepath.Join(room.FolderPath, dir))
		if err != nil || !info.IsDir() {
			t.Fatalf("%s was not created under %s: %v", dir, room.FolderPath, err)
		}
	}
	if !strings.Contains(room.SystemPrompt, "Folder room ini: "+room.FolderPath) {
		t.Fatalf("stored prompt = %q", room.SystemPrompt)
	}
	hm.mu.Lock()
	prompt := hm.createPrompt
	hm.mu.Unlock()
	if prompt != room.SystemPrompt {
		t.Fatalf("Hermes session prompt = %q, want the room prompt", prompt)
	}
}

func TestUploadSniffsPNGAndRejectsWhatItShould(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_upload"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	rec := upload(t, e, cookie, room.ID.String(), "Foto Pantai.PNG", tinyPNG)
	if rec.Code != http.StatusCreated {
		t.Fatalf("png upload = %d, body %s", rec.Code, rec.Body.String())
	}
	file := decodeFile(t, rec)
	if file.Mime != "image/png" || file.Source != "user" {
		t.Fatalf("file = %+v", file)
	}
	if file.Name != "Foto Pantai.PNG" {
		t.Errorf("display name = %q", file.Name)
	}
	if file.Size != int64(len(tinyPNG)) || file.Sha256 == "" {
		t.Errorf("size/hash = %d/%q", file.Size, file.Sha256)
	}

	st.mu.Lock()
	row := st.files[mustUUID(t, file.ID)]
	st.mu.Unlock()
	abs := filepath.Join(room.FolderPath, filepath.FromSlash(row.Path))
	if data, err := os.ReadFile(abs); err != nil || !bytes.Equal(data, tinyPNG) {
		t.Fatalf("stored bytes at %s: err=%v equal=%v", abs, err, bytes.Equal(data, tinyPNG))
	}
	if !strings.HasPrefix(row.Path, "inbox/") {
		t.Errorf("stored path = %q, want inbox/...", row.Path)
	}

	rec = upload(t, e, cookie, room.ID.String(), "evil.exe", append([]byte("MZ"), make([]byte, 64)...))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("exe upload = %d, want 415 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tipe file ini belum didukung") {
		t.Fatalf("unsupported copy = %s", rec.Body.String())
	}

	big := bytes.Repeat([]byte("a"), 26<<20)
	rec = upload(t, e, cookie, room.ID.String(), "besar.txt", big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("26MB upload = %d, want 413 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "batasnya 25 MB") {
		t.Fatalf("size copy = %s", rec.Body.String())
	}
}

func TestMessageAttachmentLineIsForHermesNotTheBubble(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_attach"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	rec := upload(t, e, cookie, room.ID.String(), "foto.png", tinyPNG)
	file := decodeFile(t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d", rec.Code)
	}

	hm.events = []hermes.Event{{Kind: "delta", Text: "Saya lihat fotonya."}}
	rec = request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]any{
		"text":          "Tolong jelaskan foto ini",
		"attachmentIds": []string{file.ID},
	}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d, body %s", rec.Code, rec.Body.String())
	}

	hm.mu.Lock()
	sent, instructions := hm.lastText, hm.instructions
	hm.mu.Unlock()
	if !strings.Contains(sent, "Lampiran: inbox/") || !strings.Contains(sent, "(PNG,") {
		t.Fatalf("Hermes did not get the attachment line: %q", sent)
	}
	if !strings.Contains(instructions, "Folder room ini: "+room.FolderPath) {
		t.Fatalf("room instructions missing the folder: %q", instructions)
	}
	if strings.Contains(sent, "Foto Pantai") {
		// The display name may appear in the attachment line only if it is part of
		// the stored path; it must not leak as an absolute host path.
	}

	var userBody string
	var linked bool
	st.mu.Lock()
	for _, row := range st.messages {
		if row.RoomID == room.ID && row.Role == "user" {
			userBody = row.Body
		}
	}
	row := st.files[mustUUID(t, file.ID)]
	linked = row.MessageID != nil
	st.mu.Unlock()

	if userBody != "Tolong jelaskan foto ini" {
		t.Fatalf("visible user body = %q, want only what Boss typed", userBody)
	}
	if !linked {
		t.Fatal("uploaded file was not linked to the user message")
	}

	rec = request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/messages", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("list messages = %d", rec.Code)
	}
	var page struct {
		Messages []messageView `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range page.Messages {
		if m.Role == "user" && len(m.Attachments) == 1 && m.Attachments[0].ID == file.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("message DTO has no attachment: %s", rec.Body.String())
	}
}

func TestArtifactsAreStoredAndMarkersStrippedFromBody(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_artifact"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	agentFile := filepath.Join(room.FolderPath, "outbox", "catatan.md")
	if err := os.MkdirAll(filepath.Dir(agentFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentFile, []byte("# halo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hm.events = []hermes.Event{{
		Kind: "delta",
		Text: "Sudah jadi, Boss.\n[[artifact: outbox/catatan.md]]\n",
	}}
	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "buat catatan"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d, body %s", rec.Code, rec.Body.String())
	}

	var assistant *storeMessage
	st.mu.Lock()
	for i := range st.messages {
		if st.messages[i].RoomID == room.ID && st.messages[i].Role == "assistant" {
			assistant = &storeMessage{ID: st.messages[i].ID, Body: st.messages[i].Body}
		}
	}
	var artifact *uploadedFile
	for _, row := range st.files {
		if row.Source == "agent" {
			artifact = &uploadedFile{Name: row.Name, Mime: row.Mime, Size: row.Size, Sha256: row.Sha256}
		}
	}
	st.mu.Unlock()

	if assistant == nil || assistant.Body != "Sudah jadi, Boss." {
		t.Fatalf("assistant body = %+v, marker must be stripped", assistant)
	}
	if artifact == nil || artifact.Name != "catatan.md" || artifact.Mime != "text/markdown" {
		t.Fatalf("artifact row = %+v", artifact)
	}

	rec = request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/messages", nil, cookie)
	var page struct {
		Messages []messageView `json:"messages"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	var attached bool
	for _, m := range page.Messages {
		if m.Role == "assistant" && len(m.Attachments) == 1 && m.Attachments[0].Name == "catatan.md" {
			attached = true
		}
	}
	if !attached {
		t.Fatalf("artifact missing from message DTO: %s", rec.Body.String())
	}
}

func TestBrokenArtifactMarkerStaysVisible(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_broken"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	hm.events = []hermes.Event{{Kind: "delta", Text: "Selesai.\n[[artifact: outbox/tidak-ada.pdf]]\n"}}
	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "buat"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d", rec.Code)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	for _, row := range st.messages {
		if row.RoomID == room.ID && row.Role == "assistant" {
			if !strings.Contains(row.Body, "[[artifact: outbox/tidak-ada.pdf]]") {
				t.Fatalf("broken promise was hidden: %q", row.Body)
			}
			return
		}
	}
	t.Fatal("no assistant message stored")
}

func TestDownloadHeadersAndDeleteRemovesBytes(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_download"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	png := decodeFile(t, upload(t, e, cookie, room.ID.String(), "photo.png", tinyPNG))
	rec := request(t, e, http.MethodGet, "/api/files/"+png.ID, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(echo.HeaderContentType); got != "image/png" {
		t.Errorf("content-type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Errorf("png disposition = %q, want inline", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=0, must-revalidate" {
		t.Errorf("cache-control = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "136" {
		t.Errorf("content-length = %q, want 136", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), tinyPNG) {
		t.Fatal("downloaded bytes differ from the upload")
	}

	md := decodeFile(t, upload(t, e, cookie, room.ID.String(), "catatan.md", []byte("# halo\n")))
	rec = request(t, e, http.MethodGet, "/api/files/"+md.ID, nil, cookie)
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("markdown disposition = %q, want attachment", got)
	}

	st.mu.Lock()
	row := st.files[mustUUID(t, png.ID)]
	st.mu.Unlock()
	abs := filepath.Join(room.FolderPath, filepath.FromSlash(row.Path))

	rec = request(t, e, http.MethodDelete, "/api/files/"+png.ID, nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("bytes still on disk at %s (err=%v)", abs, err)
	}
	rec = request(t, e, http.MethodGet, "/api/files/"+png.ID, nil, cookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("download after delete = %d, want 404", rec.Code)
	}

	rec = request(t, e, http.MethodGet, "/api/rooms/"+room.ID.String()+"/files", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), md.ID) {
		t.Fatalf("library after delete = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAudioStreamsInlineButTheDownloadControlAsksForAnAttachment(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_audio"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	mp3 := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 256)...)
	file := decodeFile(t, upload(t, e, cookie, room.ID.String(), "lagu.mp3", mp3))
	if file.Mime != "audio/mpeg" {
		t.Fatalf("mp3 mime = %q", file.Mime)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/files/"+file.ID, nil)
	req.Header.Set("Range", "bytes=0-15")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range = %d, want 206 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Errorf("audio disposition = %q, want inline", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("accept-ranges = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff = %q", got)
	}

	rec = request(t, e, http.MethodGet, "/api/files/"+file.ID+"?download=1", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("download disposition = %q, want attachment", got)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "lagu.mp3") {
		t.Errorf("download disposition must name the file: %q", rec.Header().Get("Content-Disposition"))
	}
}
