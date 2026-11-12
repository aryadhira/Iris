package hermes

import (
	"os"
	"testing"
)

// The event below is the real frame captured from the pinned gateway on
// 2026-11-12 (approval request for a cloud-metadata curl). The command is
// already redacted by Hermes; this test proves irisd parses that exact shape
// rather than an invented one.
func TestNormalizeCapturedApprovalEvent(t *testing.T) {
	raw, err := os.ReadFile("testdata/approval-request.json")
	if err != nil {
		t.Fatalf("read captured event: %v", err)
	}
	ev := normalize("approval.request", raw, nil)

	if ev.Kind != "approval" {
		t.Fatalf("Kind = %q, want approval", ev.Kind)
	}
	if ev.RunID != "run_2a1004c7eedb4e29825f5c3c5f1e3b71" {
		t.Errorf("RunID = %q", ev.RunID)
	}
	if ev.RequestID != "3902fce5059745cf90de336c6a942a00" {
		t.Errorf("RequestID = %q", ev.RequestID)
	}
	if ev.SessionID != "api_1794457951_19dd0df1" {
		t.Errorf("SessionID = %q", ev.SessionID)
	}
	if ev.MessageID != "msg_71721666a69f46edb951e953b6933356" {
		t.Errorf("MessageID = %q", ev.MessageID)
	}
	if ev.Command != "rtk curl -s --max-time 2 http://169.254.169.254/latest/meta-data/ || echo P6-PROBE-TIDAK-ADA" {
		t.Errorf("Command = %q", ev.Command)
	}
	if ev.Description != "cloud metadata endpoint access (instance credentials)" {
		t.Errorf("Description = %q", ev.Description)
	}
	if len(ev.Choices) != 4 {
		t.Fatalf("Choices = %v, want 4", ev.Choices)
	}
	want := []string{"once", "session", "always", "deny"}
	for i := range want {
		if ev.Choices[i] != want[i] {
			t.Errorf("Choices[%d] = %q, want %q", i, ev.Choices[i], want[i])
		}
	}
}
