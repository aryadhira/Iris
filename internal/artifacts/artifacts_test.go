package artifacts

import (
	"errors"
	"strings"
	"testing"
)

func TestParseToleratesSpacesAndWindowsSlashes(t *testing.T) {
	text := "Selesai.\n  [[artifact:  outbox\\laporan.docx  ]]\n[[ artifact: outbox/a b.md ]]\n"
	markers := Parse(text)
	if len(markers) != 2 {
		t.Fatalf("markers = %v, want 2", markers)
	}
	if markers[0].Path != "outbox/laporan.docx" {
		t.Errorf("first path = %q", markers[0].Path)
	}
	if markers[1].Path != "outbox/a b.md" {
		t.Errorf("second path = %q", markers[1].Path)
	}
}

func TestProcessStripsOnlyFilesThatExist(t *testing.T) {
	existing := map[string]string{
		"outbox/a.md":  "/room/outbox/a.md",
		"outbox/b.pdf": "/room/outbox/b.pdf",
	}
	resolve := func(rel string) (string, error) {
		if abs, ok := existing[rel]; ok {
			return abs, nil
		}
		return "", errors.New("missing")
	}

	text := "Dua file sudah siap.\n[[artifact: outbox/a.md]]\n[[artifact: outbox\\b.pdf]]\n[[artifact: outbox/hilang.csv]]\n"
	result := Process(text, resolve)

	if result.Body != "Dua file sudah siap.\n[[artifact: outbox/hilang.csv]]" {
		t.Fatalf("body = %q", result.Body)
	}
	if len(result.Files) != 2 {
		t.Fatalf("found = %v, want 2", result.Files)
	}
	if len(result.Broken) != 1 || result.Broken[0] != "outbox/hilang.csv" {
		t.Fatalf("broken = %v", result.Broken)
	}
	if strings.Contains(result.Body, "outbox/a.md") || strings.Contains(result.Body, "outbox/b.pdf") {
		t.Fatal("kept markers must be stripped from the body")
	}
}

func TestProcessMarkerOnlyLeavesEmptyBody(t *testing.T) {
	result := Process("[[artifact: outbox/x.txt]]\n", func(string) (string, error) {
		return "/room/outbox/x.txt", nil
	})
	if result.Body != "" {
		t.Fatalf("body = %q, want empty", result.Body)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %v", result.Files)
	}
}
