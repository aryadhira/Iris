package continuation

import (
	"testing"
	"time"
)

func TestExtractProcsFromEventsTextBothAndNone(t *testing.T) {
	t.Run("from tool event bytes", func(t *testing.T) {
		raw := `{"tool_name":"terminal","session_id":"proc_6a1c1303b005","output":"Background process started"}`
		got := ExtractProcs(raw)
		if len(got) != 1 || got[0] != "proc_6a1c1303b005" {
			t.Fatalf("ExtractProcs(event) = %v", got)
		}
	})

	t.Run("from assembled text", func(t *testing.T) {
		body := "Sudah jalan. Proses proc_20d1fd10281e berjalan.\n\nSTATUS: RUNNING — 5 detik"
		got := ExtractProcs(body)
		if len(got) != 1 || got[0] != "proc_20d1fd10281e" {
			t.Fatalf("ExtractProcs(text) = %v", got)
		}
	})

	t.Run("both, deduplicated in first-seen order", func(t *testing.T) {
		got := ExtractProcs(
			`{"output":"proc_aaa111 started"}`,
			"menunggu proc_bbb222, lalu proc_aaa111 lagi",
		)
		if len(got) != 2 || got[0] != "proc_aaa111" || got[1] != "proc_bbb222" {
			t.Fatalf("ExtractProcs(both) = %v", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		if got := ExtractProcs("tidak ada proses di sini", `{"tool_name":"terminal"}`); len(got) != 0 {
			t.Fatalf("ExtractProcs(none) = %v", got)
		}
	})
}

func TestParseStatusUsesTheLastLine(t *testing.T) {
	body := "Contoh di tengah: STATUS: DONE — bukan ini\nhasil kerja\n\nSTATUS: RUNNING — menunggu 90 detik."
	kind, line := ParseStatus(body)
	if kind != StatusRunning {
		t.Fatalf("kind = %q, want RUNNING", kind)
	}
	if line != "STATUS: RUNNING — menunggu 90 detik." {
		t.Fatalf("line = %q", line)
	}

	for body, want := range map[string]Status{
		"STATUS: DONE — selesai":           StatusDone,
		"**STATUS: BLOCKED** — butuh izin": StatusBlocked,
		"STATUS: CONTINUE — masih jalan":   StatusRunning,
		"tidak ada status":                 StatusNone,
	} {
		if got, _ := ParseStatus(body); got != want {
			t.Errorf("ParseStatus(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestDecideBounds(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	deadline := now.Add(Window)

	t.Run("RUNNING schedules", func(t *testing.T) {
		got := Decide(Input{Status: StatusRunning, Attempt: 0, Now: now, Deadline: deadline})
		if got.Kind != ActionSchedule {
			t.Fatalf("action = %+v, want schedule", got)
		}
	})

	t.Run("proc id without a status schedules", func(t *testing.T) {
		got := Decide(Input{Procs: []string{"proc_abc123"}, Attempt: 0, Now: now, Deadline: deadline})
		if got.Kind != ActionSchedule {
			t.Fatalf("action = %+v, want schedule", got)
		}
	})

	t.Run("DONE never continues", func(t *testing.T) {
		got := Decide(Input{Status: StatusDone, Procs: []string{"proc_abc123"}, Now: now, Deadline: deadline})
		if got.Kind != ActionNone {
			t.Fatalf("action = %+v, want none", got)
		}
	})

	t.Run("BLOCKED never continues", func(t *testing.T) {
		got := Decide(Input{Status: StatusBlocked, Now: now, Deadline: deadline})
		if got.Kind != ActionNone {
			t.Fatalf("action = %+v, want none", got)
		}
	})

	t.Run("three continuations, then capped", func(t *testing.T) {
		for attempt := 0; attempt < MaxPerChain; attempt++ {
			got := Decide(Input{Status: StatusRunning, Attempt: attempt, Now: now, Deadline: deadline})
			if got.Kind != ActionSchedule {
				t.Fatalf("attempt %d: action = %+v, want schedule", attempt, got)
			}
		}
		got := Decide(Input{Status: StatusRunning, Attempt: MaxPerChain, Now: now, Deadline: deadline})
		if got.Kind != ActionCapped {
			t.Fatalf("attempt %d: action = %+v, want capped", MaxPerChain, got)
		}
	})

	t.Run("30 minute cap with a fake clock", func(t *testing.T) {
		late := deadline.Add(time.Second)
		got := Decide(Input{Status: StatusRunning, Attempt: 1, Now: late, Deadline: deadline})
		if got.Kind != ActionCapped || got.Reason != "batas 30 menit tercapai" {
			t.Fatalf("action = %+v, want the 30 minute cap", got)
		}
	})

	t.Run("previous continuation without tools stops", func(t *testing.T) {
		got := Decide(Input{Status: StatusRunning, IsContinuation: true, ToolCount: 0, Attempt: 1, Now: now, Deadline: deadline})
		if got.Kind != ActionSkip {
			t.Fatalf("action = %+v, want skip", got)
		}
	})

	t.Run("archived cancels", func(t *testing.T) {
		got := Decide(Input{Status: StatusRunning, Archived: true, Now: now, Deadline: deadline})
		if got.Kind != ActionCancel {
			t.Fatalf("action = %+v, want cancel", got)
		}
	})

	t.Run("newer user message cancels", func(t *testing.T) {
		got := Decide(Input{Status: StatusRunning, NewerUserMessage: true, Now: now, Deadline: deadline})
		if got.Kind != ActionCancel {
			t.Fatalf("action = %+v, want cancel", got)
		}
	})
}

func TestMetaEmpty(t *testing.T) {
	if !(Meta{}).Empty() {
		t.Fatal("zero meta must be empty")
	}
	if (Meta{Tools: []Tool{{Name: "terminal"}}}).Empty() {
		t.Fatal("meta with a tool is not empty")
	}
}
