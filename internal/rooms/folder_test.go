package rooms

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestResolveRejectsAnythingOutsideTheRoom(t *testing.T) {
	data := t.TempDir()
	a, err := Create(data, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(data, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(b.Root, "outbox", "secret.txt")
	if err := os.WriteFile(other, []byte("b only"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(a.Inbox(), "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b.Root, filepath.Join(a.Inbox(), "other-room")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"parent traversal", "../" + filepath.Base(b.Root) + "/outbox/secret.txt"},
		{"deep traversal", "inbox/../../" + filepath.Base(b.Root) + "/outbox/secret.txt"},
		{"absolute", outside},
		{"absolute unix", "/etc/passwd"},
		{"root itself", "."},
		{"empty", ""},
		{"symlink to file", "inbox/escape"},
		{"symlink to another room", "inbox/other-room/outbox/secret.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := a.Resolve(tc.path); err == nil {
				t.Fatalf("Resolve(%q) = %q, want an error", tc.path, got)
			} else if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Resolve(%q) error = %v, want ErrUnsafePath", tc.path, err)
			}
		})
	}

	ok := filepath.Join("inbox", "photo.png")
	got, err := a.Resolve(ok)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", ok, err)
	}
	if got != filepath.Join(a.Root, ok) {
		t.Fatalf("Resolve(%q) = %q", ok, got)
	}

	missing, err := a.Resolve("outbox/not-yet.md")
	if err != nil {
		t.Fatalf("a missing file inside the room must resolve: %v", err)
	}
	if !strings.HasPrefix(missing, a.Root+string(os.PathSeparator)) {
		t.Fatalf("missing path %q is not under %q", missing, a.Root)
	}
}

func TestContinuityContractIsOnEveryTurn(t *testing.T) {
	folder, err := Create(t.TempDir(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	prompt := folder.SystemPrompt()
	for _, want := range []string{
		"process_manage wait",
		"STATUS: RUNNING",
		"STATUS: DONE",
		"STATUS: BLOCKED",
		"Jangan pernah mengaku selesai",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("SystemPrompt is missing %q", want)
		}
	}

	// A room created before P3 has a stored contract without the new lines.
	old := "Kamu adalah Hermes, bekerja di dalam room Iris ini.\nFolder room ini: /tmp/x."
	turn := TurnInstructions(old)
	if !strings.Contains(turn, old) || !strings.Contains(turn, ContinuityPrompt) {
		t.Fatalf("TurnInstructions did not append the contract: %q", turn)
	}
	if strings.Count(turn, ContinuityPrompt) != 1 {
		t.Fatalf("continuity contract appears more than once")
	}
	if got := TurnInstructions(prompt); got != prompt {
		t.Fatalf("TurnInstructions duplicated a contract that already has it")
	}
}

func TestSanitizeFilenameNeverLeavesAPath(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":      "passwd",
		`C:\Users\boss\a.docx`:  "a.docx",
		"  laporan akhir.pdf  ": "laporan akhir.pdf",
		"":                      "berkas",
		"..":                    "berkas",
	}
	for input, want := range cases {
		if got := SanitizeFilename(input); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", input, got, want)
		}
	}
}
