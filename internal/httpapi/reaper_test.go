package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aryadhira/iris/internal/live"
	"github.com/aryadhira/iris/internal/store"
)

// A hung turn used to hold its room hostage forever: the guard read "running" and every new message was
// refused with "Room ini masih bekerja" until a human fixed the database. The reaper is the safety net.
func TestReaperClosesAStaleRunAndSaysSo(t *testing.T) {
	roomID := uuid.New()
	runID := "run_stale"
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)

	fake := newFakeStore()
	srv := &Server{store: fake, logger: testLogger(), hub: live.New()}
	fake.runs[runID] = store.Run{
		RunID:     runID,
		RoomID:    roomID,
		SessionID: "s1",
		Status:    "running",
		StartedAt: now.Add(-45 * time.Minute),
		UpdatedAt: now.Add(-45 * time.Minute),
	}
	srv.now = func() time.Time { return now }

	srv.reapStaleRuns(context.Background())

	if got := fake.runs[runID].Status; got != "interrupted" {
		t.Fatalf("run status = %q, want interrupted", got)
	}
	noted := false
	for _, m := range fake.messages {
		if m.RoomID == roomID && m.Role == "system" {
			noted = true
		}
	}
	if !noted {
		t.Fatal("the room must explain why the turn closed, not close it silently")
	}
}

// A run that is still within its silence budget must be left alone.
func TestReaperLeavesAFreshRunAlone(t *testing.T) {
	roomID := uuid.New()
	runID := "run_fresh"
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)

	fake := newFakeStore()
	srv := &Server{store: fake, logger: testLogger(), hub: live.New()}
	fake.runs[runID] = store.Run{
		RunID:     runID,
		RoomID:    roomID,
		Status:    "running",
		StartedAt: now.Add(-2 * time.Minute),
		UpdatedAt: now.Add(-2 * time.Minute),
	}
	srv.now = func() time.Time { return now }

	srv.reapStaleRuns(context.Background())

	if got := fake.runs[runID].Status; got != "running" {
		t.Fatalf("a 2-minute-old run was reaped: status = %q", got)
	}
}
