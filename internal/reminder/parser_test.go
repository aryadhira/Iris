package reminder

import (
	"testing"
	"time"
)

func wib() *time.Location { return time.FixedZone("WIB", 7*60*60) }

func TestParseIndonesianReminderSentences(t *testing.T) {
	loc := wib()
	// Friday, 13 November 2026, 10:00 WIB.
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, loc)
	tomorrow := now.AddDate(0, 0, 1)

	cases := []struct {
		name string
		text string
		ok   bool
		due  time.Time
		want string
	}{
		{
			name: "jam 9 is 09:00, not 21:00",
			text: "ingatkan saya jam 9",
			ok:   true,
			due:  time.Date(2026, 11, 14, 9, 0, 0, 0, loc), // today 09:00 already passed
			want: "waktunya",
		},
		{
			name: "today when the hour is still ahead",
			text: "ingatkan saya jam 15.30 telepon Budi",
			ok:   true,
			due:  time.Date(2026, 11, 13, 15, 30, 0, 0, loc),
			want: "telepon Budi",
		},
		{
			name: "malam moves a 12-hour clock to the evening",
			text: "ingetin aku jam 9 malam",
			ok:   true,
			due:  time.Date(2026, 11, 13, 21, 0, 0, 0, loc),
			want: "waktunya",
		},
		{
			name: "sore moves to the afternoon",
			text: "ingatkan saya nanti jam 3 sore buat laporan",
			ok:   true,
			due:  time.Date(2026, 11, 13, 15, 0, 0, 0, loc),
			want: "laporan",
		},
		{
			name: "siang moves 1 to 13",
			text: "tolong ingatkan saya jam 1 siang",
			ok:   true,
			due:  time.Date(2026, 11, 13, 13, 0, 0, 0, loc),
			want: "waktunya",
		},
		{
			name: "besok is the next calendar day",
			text: "ingatkan saya besok jam 8 untuk rapat",
			ok:   true,
			due:  time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 8, 0, 0, 0, loc),
			want: "rapat",
		},
		{
			name: "besok with pagi",
			text: "ingetin saya besok jam 9 pagi antar dokumen",
			ok:   true,
			due:  time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 9, 0, 0, 0, loc),
			want: "antar dokumen",
		},
		{
			name: "dalam menit is relative",
			text: "ingatkan saya dalam 30 menit cek oven",
			ok:   true,
			due:  now.Add(30 * time.Minute),
			want: "cek oven",
		},
		{
			name: "dalam jam is relative",
			text: "ingetin dalam 2 jam buat laporan",
			ok:   true,
			due:  now.Add(2 * time.Hour),
			want: "laporan",
		},
		{
			name: "dalam detik is relative",
			text: "ingatkan saya dalam 45 detik",
			ok:   true,
			due:  now.Add(45 * time.Second),
			want: "waktunya",
		},
		{
			name: "pagi already passed rolls to tomorrow",
			text: "ingatkan saya jam 7 pagi minum obat",
			ok:   true,
			due:  time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 7, 0, 0, 0, loc),
			want: "minum obat",
		},
		{
			name: "jam 12 malam is midnight",
			text: "ingatkan saya jam 12 malam",
			ok:   true,
			due:  time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, loc),
			want: "waktunya",
		},
		{
			name: "a time without the trigger creates nothing",
			text: "jam 3 saja",
			ok:   false,
		},
		{
			name: "a trigger without a time creates nothing",
			text: "ingatkan saya nanti",
			ok:   false,
		},
		{
			name: "an impossible hour creates nothing",
			text: "ingatkan saya jam 25",
			ok:   false,
		},
		{
			name: "an impossible minute creates nothing",
			text: "ingatkan saya jam 9:75",
			ok:   false,
		},
		{
			name: "punctuation and filler do not become the subject",
			text: "Ingatkan saya jam 3 sore, ya",
			ok:   true,
			due:  time.Date(2026, 11, 13, 15, 0, 0, 0, loc),
			want: "waktunya",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Parse(tc.text, now, loc)
			if ok != tc.ok {
				t.Fatalf("Parse(%q) ok = %v, want %v (got %+v)", tc.text, ok, tc.ok, got)
			}
			if !tc.ok {
				return
			}
			if !got.DueAt.Equal(tc.due) {
				t.Errorf("due = %s, want %s", got.DueAt.Format(time.RFC3339), tc.due.Format(time.RFC3339))
			}
			if got.Text != tc.want {
				t.Errorf("text = %q, want %q", got.Text, tc.want)
			}
		})
	}
}

func TestJakartaHasTheRightOffset(t *testing.T) {
	loc := Jakarta()
	_, offset := time.Date(2026, 11, 13, 0, 0, 0, 0, loc).Zone()
	if offset != 7*60*60 {
		t.Fatalf("Asia/Jakarta offset = %d, want 25200", offset)
	}
}
