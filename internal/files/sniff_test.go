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
