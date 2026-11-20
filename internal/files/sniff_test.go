package files

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectFileUsesBytesNotTheClientType(t *testing.T) {
	dir := t.TempDir()
	png := write(t, dir, "photo.png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...))
	pdf := write(t, dir, "doc.pdf", []byte("%PDF-1.7\n1 0 obj\n"))
	json := write(t, dir, "data.json", []byte(`{"ok":true}`))
	md := write(t, dir, "notes.md", []byte("# halo\n"))
	csv := write(t, dir, "rows.csv", []byte("a,b\n1,2\n"))
	exe := write(t, dir, "evil.exe", append([]byte("MZ"), make([]byte, 64)...))

	cases := map[string]string{
		png:  MIMEPNG,
		pdf:  MIMEPDF,
		json: MIMEJSON,
		md:   MIMEMD,
		csv:  MIMECSV,
		exe:  MIMEOther,
	}
	for path, want := range cases {
		got, err := DetectFile(path, filepath.Base(path))
		if err != nil {
			t.Fatalf("DetectFile(%s): %v", path, err)
		}
		if got != want {
			t.Errorf("DetectFile(%s) = %q, want %q", filepath.Base(path), got, want)
		}
	}
	if Allowed(MIMEOther) {
		t.Fatal("application/octet-stream must not be accepted for upload")
	}
}

func TestDetectFileTellsDocxAndXlsxFromAPlainZip(t *testing.T) {
	dir := t.TempDir()
	docx := filepath.Join(dir, "letter.docx")
	makeZip(t, docx, map[string]string{"word/document.xml": "<w:document/>"})
	xlsx := filepath.Join(dir, "sheet.xlsx")
	makeZip(t, xlsx, map[string]string{"xl/workbook.xml": "<workbook/>"})
	plain := filepath.Join(dir, "bundle.zip")
	makeZip(t, plain, map[string]string{"readme.txt": "hi"})

	cases := map[string]string{docx: MIMEDOCX, xlsx: MIMEXLSX, plain: MIMEZip}
	for path, want := range cases {
		got, err := DetectFile(path, filepath.Base(path))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", filepath.Base(path), got, want)
		}
		if !Allowed(got) {
			t.Errorf("%s should be allowed", filepath.Base(path))
		}
	}
}

func TestFormatSizeAndHumanType(t *testing.T) {
	cases := map[int64]string{
		840:              "840 B",
		12 * 1024:        "12 KB",
		1280 * 1024:      "1,2 MB",
		26 * 1024 * 1024: "26,0 MB",
	}
	for size, want := range cases {
		if got := FormatSize(size); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", size, got, want)
		}
	}
	if got := HumanType(MIMEPDF, "a.pdf"); got != "PDF" {
		t.Errorf("HumanType(pdf) = %q", got)
	}
	if got := HumanType(MIMEDOCX, "a.docx"); got != "DOCX" {
		t.Errorf("HumanType(docx) = %q", got)
	}
}

func makeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// http.DetectContentType answers application/octet-stream for every ISO base media file, which is why an
// ordinary mp4 was rejected as "not supported". These cases pin the magic-byte path.
func TestDetectFileRecognisesVideoByMagicBytes(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"clip.mp4", ftyp("isom"), MIMEMP4},
		{"clip.mp4", ftyp("mp42"), MIMEMP4},
		{"clip.m4v", ftyp("M4V "), MIMEMP4},
		{"clip.mov", ftyp("qt  "), MIMEMOV},
		{"clip.mov", ftyp("isom"), MIMEMOV}, // generic brand with a .mov name
		{"clip.webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x01, 0x00, 0x00, 0x00}, MIMEWEBM},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, tc.name)
		body := append(tc.head, make([]byte, 200)...)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := DetectFile(path, tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s (%s): got %s, want %s", tc.name, string(tc.head[8:12]), got, tc.want)
		}
		if !Allowed(got) {
			t.Fatalf("%s: %s must be allowed", tc.name, got)
		}
		if !IsVideo(got) {
			t.Fatalf("%s: %s must count as video for the larger ceiling", tc.name, got)
		}
	}
}

func TestRandomBytesAreNotMistakenForVideo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.mp4")
	if err := os.WriteFile(path, []byte("this is not a video, it is text pretending to be one"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DetectFile(path, "fake.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if IsVideo(got) {
		t.Fatalf("text named .mp4 was sniffed as %s", got)
	}
}

// ftyp builds an ISO base media header: 4-byte size, "ftyp", brand, minor version.
func ftyp(brand string) []byte {
	head := make([]byte, 16)
	head[3] = 0x18
	copy(head[4:8], "ftyp")
	copy(head[8:12], brand)
	return head
}

func TestDetectFileRecognisesSoundAndAcceptsIt(t *testing.T) {
	wavHead := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"lagu.mp3", []byte("ID3\x04\x00\x00\x00\x00\x00\x00"), MIMEMP3},
		{"frame.mp3", []byte{0xFF, 0xFB, 0x90, 0x64, 0x00, 0x00}, MIMEMP3},
		{"lagu.m4a", ftyp("M4A "), MIMEM4A},
		{"lagu.m4a", ftyp("isom"), MIMEM4A},
		{"suara.ogg", []byte("OggS\x00\x02\x00\x00"), MIMEOGG},
		{"suara.wav", wavHead, MIMEWAV},
		{"suara.flac", []byte("fLaC\x00\x00\x00\x22"), MIMEFLAC},
		{"suara.aac", []byte{0xFF, 0xF1, 0x50, 0x80, 0x00, 0x1F, 0xFC}, MIMEAAC},
		{"suara.weba", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x01, 0x00, 0x00, 0x00}, MIMEAudioWebM},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, tc.name)
		body := append(tc.head, make([]byte, 200)...)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := DetectFile(path, tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %s, want %s", tc.name, got, tc.want)
		}
		if !Allowed(got) {
			t.Fatalf("%s: %s must be allowed", tc.name, got)
		}
		if !IsAudio(got) {
			t.Fatalf("%s: %s must count as audio", tc.name, got)
		}
		if !IsMedia(got) {
			t.Fatalf("%s: %s must use the media ceiling", tc.name, got)
		}
	}
}

func TestM4ABrandIsNotCalledVideo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lagu.m4a")
	if err := os.WriteFile(path, append(ftyp("M4A "), make([]byte, 100)...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DetectFile(path, "lagu.m4a")
	if err != nil {
		t.Fatal(err)
	}
	if got != MIMEM4A {
		t.Fatalf("M4A brand = %q, want %q", got, MIMEM4A)
	}
	if IsVideo(got) {
		t.Fatal("an M4A song must not be classified as video")
	}
}

func TestMediaHelpersAndInlineRule(t *testing.T) {
	for _, mimeType := range []string{MIMEMP3, MIMEM4A, MIMEOGG, MIMEWAV, MIMEAudioWebM, MIMEFLAC, MIMEAAC, MIMEMP4, MIMEMOV, MIMEWEBM} {
		if !IsMedia(mimeType) {
			t.Errorf("%s must use the media ceiling", mimeType)
		}
		if !Inline(mimeType) {
			t.Errorf("%s must stream inline", mimeType)
		}
	}
	for _, mimeType := range []string{MIMEPDF, MIMEPNG, MIMEText} {
		if IsMedia(mimeType) {
			t.Errorf("%s must not use the media ceiling", mimeType)
		}
	}
	if !IsVideo(MIMEMP4) || IsVideo(MIMEMP3) {
		t.Error("IsVideo must stay true for video and false for sound")
	}
	if !IsAudio(MIMEMP3) || IsAudio(MIMEMP4) {
		t.Error("IsAudio must distinguish sound from video")
	}
	if !Inline(MIMEPNG) || !Inline(MIMEPDF) {
		t.Error("the existing image/PDF inline rule must not regress")
	}
	if Inline(MIMEText) || Inline(MIMEZip) {
		t.Error("documents must still download")
	}
}
