package reply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aryadhira/iris/internal/continuation"
)

func TestCleanTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "status line removed",
			in:   "Sudah selesai.\n\nSTATUS: DONE — laporan ada di outbox.\n",
			want: "Sudah selesai.",
		},
		{
			name: "markdown-wrapped status removed",
			in:   "**STATUS: BLOCKED** — butuh izin\nTeks lain.",
			want: "Teks lain.",
		},
		{
			name: "status at the start leaves no leading blank",
			in:   "STATUS: RUNNING — 5 detik\n\nTeks lain.",
			want: "Teks lain.",
		},
		{
			name: "status in the middle leaves one paragraph break",
			in:   "Satu.\n\nSTATUS: DONE — selesai\n\nDua.",
			want: "Satu.\n\nDua.",
		},
		{
			name: "standalone double dash becomes em dash",
			in:   "Langkah pertama -- lalu langkah kedua.",
			want: "Langkah pertama — lalu langkah kedua.",
		},
		{
			name: "line-start double dash becomes em dash",
			in:   "-- catatan penting\nSelesai.",
			want: "— catatan penting\nSelesai.",
		},
		{
			name: "cli flag is untouched",
			in:   "Jalankan `iris --auto --force` sekarang.",
			want: "Jalankan `iris --auto --force` sekarang.",
		},
		{
			name: "bare cli flag outside code is untouched",
			in:   "Jalankan --auto dan --force.",
			want: "Jalankan --auto dan --force.",
		},
		{
			name: "fenced comment is untouched",
			in:   "Contoh:\n```sql\n-- catatan\nSELECT 1;\n```\nSelesai.",
			want: "Contoh:\n```sql\n-- catatan\nSELECT 1;\n```\nSelesai.",
		},
		{
			name: "status inside a fence is untouched",
			in:   "```\nSTATUS: DONE — contoh\n```",
			want: "```\nSTATUS: DONE — contoh\n```",
		},
		{
			name: "horizontal rule and its blank line removed",
			in:   "Satu.\n\n---\n\nDua.",
			want: "Satu.\n\nDua.",
		},
		{
			name: "long rule removed",
			in:   "Satu.\n-----\nDua.",
			want: "Satu.\nDua.",
		},
		{
			name: "artifact marker removed",
			in:   "Selesai.\n[[artifact: outbox/a.md]]\n",
			want: "Selesai.",
		},
		{
			name: "status inside inline code is untouched",
			in:   "Contoh `STATUS: DONE` bukan status.",
			want: "Contoh `STATUS: DONE` bukan status.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Clean(tc.in)
			if got != tc.want {
				t.Fatalf("Clean() = %q, want %q", got, tc.want)
			}
			if again := Clean(got); again != got {
				t.Fatalf("Clean is not idempotent: first %q, second %q", got, again)
			}
		})
	}
}

func TestStatusIsParsedBeforeItIsCleaned(t *testing.T) {
	raw := "Pekerjaan berjalan.\n\nSTATUS: RUNNING — 90 detik"
	kind, line := continuation.ParseStatus(raw)
	if kind != continuation.StatusRunning {
		t.Fatalf("pump status = %q, want RUNNING", kind)
	}
	if line != "STATUS: RUNNING — 90 detik" {
		t.Fatalf("pump line = %q", line)
	}
	if got := Clean(raw); strings.Contains(got, "STATUS:") {
		t.Fatalf("cleaned body still has the status: %q", got)
	}
}

func TestCleanSnippetHasNoStatus(t *testing.T) {
	body := "Kesimpulan: kapal berlayar.\nSTATUS: DONE — selesai"
	got := CleanSnippet("... Kesimpulan: kapal berlayar. STATUS: DONE — selesai ...")
	if strings.Contains(got, "STATUS:") {
		t.Fatalf("snippet still contains STATUS: %q", got)
	}
	if !strings.Contains(got, "kapal berlayar") {
		t.Fatalf("snippet lost the useful text: %q", got)
	}
	if got := Clean(body); strings.Contains(got, "STATUS:") {
		t.Fatalf("body still contains STATUS: %q", got)
	}
}

func TestStreamHoldsAmbiguousTail(t *testing.T) {
	var s Stream
	// "--" must not flash as a hyphen pair before the next chunk decides.
	if got := s.Feed("Halo --"); got != "Halo " {
		t.Fatalf("first feed = %q, want %q", got, "Halo ")
	}
	if got := s.Feed(" auto"); got != "— auto" {
		t.Fatalf("second feed = %q, want %q", got, "— auto")
	}
	if got := s.Flush(); got != "" {
		t.Fatalf("flush = %q, want empty", got)
	}
}

func TestStreamHoldsStatusUntilItIsComplete(t *testing.T) {
	var s Stream
	if got := s.Feed("Selesai.\n\nSTATUS: RUN"); got != "Selesai.\n\n" {
		t.Fatalf("feed = %q, want only the prose", got)
	}
	if got := s.Feed("NING — 90 detik\n"); got != "" {
		t.Fatalf("feed = %q, want the status line withheld", got)
	}
	if got := s.Flush(); got != "" {
		t.Fatalf("flush = %q, want empty", got)
	}
	if s.Visible() != "Selesai.\n\n" {
		t.Fatalf("visible = %q", s.Visible())
	}
}

func TestStreamSplitsWithinAnOrdinarySentence(t *testing.T) {
	var s Stream
	if got := s.Feed("Hel"); got != "Hel" {
		t.Fatalf("first = %q", got)
	}
	if got := s.Feed("lo"); got != "lo" {
		t.Fatalf("second = %q", got)
	}
	if got := s.Flush(); got != "" {
		t.Fatalf("flush = %q", got)
	}
}

func TestFixturesAreIdempotent(t *testing.T) {
	files, err := filepath.Glob("../../testdata/replies/*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no reply fixtures found")
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		once := Clean(string(raw))
		twice := Clean(once)
		if once != twice {
			t.Fatalf("%s is not idempotent:\nfirst:  %q\nsecond: %q", file, once, twice)
		}
	}
}
