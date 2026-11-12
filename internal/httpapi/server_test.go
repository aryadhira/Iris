package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/auth"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/store"
)

const testPassphrase = "correct horse battery staple"

type fakeStore struct {
	mu sync.Mutex

	rooms    map[uuid.UUID]store.Room
	messages []store.Message
	files    map[uuid.UUID]store.File
	order    []uuid.UUID
	sessions map[string]store.AuthSession

	continuations []store.Continuation
	approvals     []store.Approval
	runs          map[string]store.Run
	reminders     []store.Reminder
	pushSubs      []store.PushSubscription

	createRoomCalls int
	lastRoomArg     store.CreateRoomParams
	failCreateRoom  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		rooms:    map[uuid.UUID]store.Room{},
		files:    map[uuid.UUID]store.File{},
		sessions: map[string]store.AuthSession{},
		runs:     map[string]store.Run{},
	}
}

func (f *fakeStore) CreateRoom(_ context.Context, arg store.CreateRoomParams) (store.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createRoomCalls++
	f.lastRoomArg = arg
	if f.failCreateRoom != nil {
		return store.Room{}, f.failCreateRoom
	}
	row := store.Room{
		ID:              arg.ID,
		Name:            arg.Name,
		Topic:           arg.Topic,
		HermesSessionID: arg.HermesSessionID,
		CreatedAt:       time.Now(),
		FolderPath:      arg.FolderPath,
		SystemPrompt:    arg.SystemPrompt,
	}
	f.rooms[row.ID] = row
	return row, nil
}

func (f *fakeStore) ListRooms(context.Context) ([]store.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Room, 0, len(f.rooms))
	for _, row := range f.rooms {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeStore) GetRoom(_ context.Context, id uuid.UUID) (store.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rooms[id]
	if !ok {
		return store.Room{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeStore) ArchiveRoom(_ context.Context, id uuid.UUID) (store.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rooms[id]
	if !ok {
		return store.Room{}, pgx.ErrNoRows
	}
	now := time.Now()
	row.ArchivedAt = &now
	f.rooms[id] = row
	return row, nil
}

func (f *fakeStore) UpdateRoomPrompt(_ context.Context, arg store.UpdateRoomPromptParams) (store.Room, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rooms[arg.ID]
	if !ok {
		return store.Room{}, pgx.ErrNoRows
	}
	row.SystemPrompt = arg.SystemPrompt
	f.rooms[arg.ID] = row
	return row, nil
}

func (f *fakeStore) InsertFile(_ context.Context, arg store.InsertFileParams) (store.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := store.File{
		ID:        arg.ID,
		RoomID:    arg.RoomID,
		MessageID: arg.MessageID,
		Source:    arg.Source,
		Name:      arg.Name,
		Path:      arg.Path,
		Mime:      arg.Mime,
		Size:      arg.Size,
		Sha256:    arg.Sha256,
		CreatedAt: time.Now(),
	}
	f.files[row.ID] = row
	f.order = append([]uuid.UUID{row.ID}, f.order...)
	return row, nil
}

func (f *fakeStore) GetFile(_ context.Context, id uuid.UUID) (store.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.files[id]
	if !ok {
		return store.File{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeStore) ListFiles(_ context.Context, roomID uuid.UUID) ([]store.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.File, 0)
	for _, id := range f.order {
		row := f.files[id]
		if row.RoomID == roomID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteFile(_ context.Context, id uuid.UUID) (store.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.files[id]
	if !ok {
		return store.File{}, pgx.ErrNoRows
	}
	delete(f.files, id)
	for i, existing := range f.order {
		if existing == id {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
	return row, nil
}

func (f *fakeStore) AttachFileToMessage(_ context.Context, arg store.AttachFileToMessageParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.files[arg.ID]
	if !ok || row.RoomID != arg.RoomID || row.Source != "user" || row.MessageID != nil {
		return 0, nil
	}
	row.MessageID = arg.MessageID
	f.files[row.ID] = row
	return 1, nil
}

func (f *fakeStore) InsertMessage(_ context.Context, arg store.InsertMessageParams) (store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := store.Message{
		ID:        arg.ID,
		RoomID:    arg.RoomID,
		Role:      arg.Role,
		Body:      arg.Body,
		RunID:     arg.RunID,
		Meta:      arg.Meta,
		CreatedAt: time.Now(),
	}
	f.messages = append(f.messages, row)
	return row, nil
}

func (f *fakeStore) ListMessages(_ context.Context, arg store.ListMessagesParams) ([]store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Message, 0)
	for i := len(f.messages) - 1; i >= 0; i-- {
		row := f.messages[i]
		if row.RoomID != arg.RoomID {
			continue
		}
		if arg.Before != nil && !row.CreatedAt.Before(*arg.Before) {
			continue
		}
		out = append(out, row)
		if len(out) >= int(arg.RowLimit) {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) ListAllMessages(_ context.Context, roomID uuid.UUID) ([]store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Message, 0)
	for _, row := range f.messages {
		if row.RoomID == roomID {
			out = append(out, row)
		}
	}
	return out, nil
}

// SearchMessages mirrors the real query closely enough for handler tests:
// case-insensitive substring, newest first, with the match wrapped in <mark>.
func (f *fakeStore) SearchMessages(_ context.Context, arg store.SearchMessagesParams) ([]store.SearchMessagesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	needle := strings.ToLower(strings.TrimSpace(arg.Query))
	out := make([]store.SearchMessagesRow, 0)
	for i := len(f.messages) - 1; i >= 0 && len(out) < int(arg.RowLimit); i-- {
		row := f.messages[i]
		lower := strings.ToLower(row.Body)
		index := strings.Index(lower, needle)
		if needle == "" || index < 0 {
			continue
		}
		snippet := row.Body[max(0, index-40):index] + "<mark>" +
			row.Body[index:index+len(needle)] + "</mark>" +
			row.Body[index+len(needle):min(len(row.Body), index+len(needle)+40)]
		roomName := ""
		if room, ok := f.rooms[row.RoomID]; ok {
			roomName = room.Name
		}
		out = append(out, store.SearchMessagesRow{
			ID:        row.ID,
			RoomID:    row.RoomID,
			RoomName:  roomName,
			Role:      row.Role,
			CreatedAt: row.CreatedAt,
			Snippet:   []byte(snippet),
		})
	}
	return out, nil
}

func (f *fakeStore) GetMessage(_ context.Context, id uuid.UUID) (store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.messages {
		if row.ID == id {
			return row, nil
		}
	}
	return store.Message{}, pgx.ErrNoRows
}

func (f *fakeStore) GetLatestAssistantMessage(_ context.Context, roomID uuid.UUID) (store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.messages) - 1; i >= 0; i-- {
		row := f.messages[i]
		if row.RoomID == roomID && row.Role == "assistant" {
			return row, nil
		}
	}
	return store.Message{}, pgx.ErrNoRows
}

func (f *fakeStore) CreateContinuation(_ context.Context, arg store.CreateContinuationParams) (store.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.continuations {
		if row.ChainID == arg.ChainID && row.Attempt == arg.Attempt {
			return store.Continuation{}, errors.New("duplicate chain attempt")
		}
	}
	row := store.Continuation{
		ID:        arg.ID,
		RoomID:    arg.RoomID,
		ChainID:   arg.ChainID,
		Attempt:   arg.Attempt,
		Status:    "pending",
		DueAt:     arg.DueAt,
		Deadline:  arg.Deadline,
		CreatedAt: time.Now(),
	}
	f.continuations = append(f.continuations, row)
	return row, nil
}

func (f *fakeStore) GetContinuation(_ context.Context, id uuid.UUID) (store.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.continuations {
		if row.ID == id {
			return row, nil
		}
	}
	return store.Continuation{}, pgx.ErrNoRows
}

func (f *fakeStore) ListPendingContinuations(_ context.Context, roomID uuid.UUID) ([]store.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Continuation, 0)
	for _, row := range f.continuations {
		if row.RoomID == roomID && (row.Status == "pending" || row.Status == "running") {
			out = append(out, row)
		}
	}
	sortContinuations(out)
	return out, nil
}

func (f *fakeStore) ListContinuationsForChain(_ context.Context, chainID uuid.UUID) ([]store.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Continuation, 0)
	for _, row := range f.continuations {
		if row.ChainID == chainID {
			out = append(out, row)
		}
	}
	sortContinuations(out)
	return out, nil
}

func (f *fakeStore) CountContinuationsForChain(_ context.Context, chainID uuid.UUID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var count int64
	for _, row := range f.continuations {
		if row.ChainID == chainID {
			count++
		}
	}
	return count, nil
}

func (f *fakeStore) ClaimDueContinuations(_ context.Context, arg store.ClaimDueContinuationsParams) ([]store.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Continuation, 0)
	for i := range f.continuations {
		row := &f.continuations[i]
		if row.Status != "pending" {
			continue
		}
		if arg.StartedAt != nil && row.DueAt.After(*arg.StartedAt) {
			continue
		}
		started := time.Now()
		if arg.StartedAt != nil {
			started = *arg.StartedAt
		}
		row.Status = "running"
		row.StartedAt = &started
		out = append(out, *row)
		if len(out) >= int(arg.Limit) {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) FinishContinuation(_ context.Context, arg store.FinishContinuationParams) (store.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.continuations {
		if f.continuations[i].ID == arg.ID {
			f.continuations[i].Status = arg.Status
			f.continuations[i].Reason = arg.Reason
			f.continuations[i].FinishedAt = arg.FinishedAt
			return f.continuations[i], nil
		}
	}
	return store.Continuation{}, pgx.ErrNoRows
}

func (f *fakeStore) CancelPendingContinuationsForRoom(_ context.Context, arg store.CancelPendingContinuationsForRoomParams) ([]store.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Continuation, 0)
	for i := range f.continuations {
		row := &f.continuations[i]
		if row.RoomID != arg.RoomID || (row.Status != "pending" && row.Status != "running") {
			continue
		}
		row.Status = "cancelled"
		row.Reason = arg.Reason
		row.FinishedAt = arg.FinishedAt
		out = append(out, *row)
	}
	return out, nil
}

func (f *fakeStore) ResumeRunningContinuations(_ context.Context) ([]store.Continuation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Continuation, 0)
	for i := range f.continuations {
		if f.continuations[i].Status == "running" {
			f.continuations[i].Status = "pending"
			f.continuations[i].StartedAt = nil
			out = append(out, f.continuations[i])
		}
	}
	return out, nil
}

func (f *fakeStore) HasNewerUserMessage(_ context.Context, arg store.HasNewerUserMessageParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.messages {
		if row.RoomID == arg.RoomID && row.Role == "user" && row.CreatedAt.After(arg.CreatedAt) {
			return true, nil
		}
	}
	return false, nil
}

func sortContinuations(rows []store.Continuation) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].DueAt.Before(rows[j-1].DueAt); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func (f *fakeStore) CreateAuthSession(_ context.Context, arg store.CreateAuthSessionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[arg.TokenHash] = store.AuthSession{
		TokenHash: arg.TokenHash,
		CreatedAt: time.Now(),
		ExpiresAt: arg.ExpiresAt,
		UserAgent: arg.UserAgent,
	}
	return nil
}

func (f *fakeStore) GetAuthSession(_ context.Context, tokenHash string) (store.AuthSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.sessions[tokenHash]
	if !ok || !row.ExpiresAt.After(time.Now()) {
		return store.AuthSession{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeStore) DeleteAuthSession(_ context.Context, tokenHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, tokenHash)
	return nil
}

func (f *fakeStore) UpsertRun(_ context.Context, arg store.UpsertRunParams) (store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.runs[arg.RunID]
	if !ok {
		row = store.Run{RunID: arg.RunID, StartedAt: arg.StartedAt}
	}
	row.RoomID = arg.RoomID
	row.SessionID = arg.SessionID
	row.Status = arg.Status
	row.UpdatedAt = arg.StartedAt
	f.runs[arg.RunID] = row
	return row, nil
}

func (f *fakeStore) UpdateRunStatus(_ context.Context, arg store.UpdateRunStatusParams) (store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.runs[arg.RunID]
	if !ok {
		return store.Run{}, pgx.ErrNoRows
	}
	row.Status = arg.Status
	row.UpdatedAt = arg.UpdatedAt
	row.FinishedAt = arg.FinishedAt
	f.runs[arg.RunID] = row
	return row, nil
}

func (f *fakeStore) GetRun(_ context.Context, runID string) (store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.runs[runID]
	if !ok {
		return store.Run{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeStore) ListActiveRuns(_ context.Context, roomID uuid.UUID) ([]store.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Run, 0)
	for _, row := range f.runs {
		if row.RoomID != roomID {
			continue
		}
		if row.Status == "queued" || row.Status == "running" || row.Status == "waiting_for_approval" {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) UpsertApproval(_ context.Context, arg store.UpsertApprovalParams) (store.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.approvals {
		if f.approvals[i].RunID == arg.RunID && f.approvals[i].RequestID == arg.RequestID {
			f.approvals[i].ToolName = arg.ToolName
			f.approvals[i].Command = arg.Command
			f.approvals[i].Description = arg.Description
			f.approvals[i].Choices = arg.Choices
			return f.approvals[i], nil
		}
	}
	row := store.Approval{
		ID:          arg.ID,
		RoomID:      arg.RoomID,
		RunID:       arg.RunID,
		SessionID:   arg.SessionID,
		RequestID:   arg.RequestID,
		ToolName:    arg.ToolName,
		Command:     arg.Command,
		Description: arg.Description,
		Choices:     arg.Choices,
		State:       "pending",
		CreatedAt:   arg.CreatedAt,
		ExpiresAt:   arg.ExpiresAt,
	}
	f.approvals = append(f.approvals, row)
	return row, nil
}

func (f *fakeStore) GetApproval(_ context.Context, id uuid.UUID) (store.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.approvals {
		if row.ID == id {
			return row, nil
		}
	}
	return store.Approval{}, pgx.ErrNoRows
}

func (f *fakeStore) ListApprovals(_ context.Context, roomID uuid.UUID) ([]store.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Approval, 0)
	for _, row := range f.approvals {
		if row.RoomID == roomID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) ResolveApproval(_ context.Context, arg store.ResolveApprovalParams) (store.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.approvals {
		if f.approvals[i].ID == arg.ID {
			if f.approvals[i].State != "pending" {
				return store.Approval{}, pgx.ErrNoRows
			}
			f.approvals[i].State = arg.State
			f.approvals[i].Choice = arg.Choice
			f.approvals[i].ResolvedAt = arg.ResolvedAt
			return f.approvals[i], nil
		}
	}
	return store.Approval{}, pgx.ErrNoRows
}

func (f *fakeStore) ExpireApprovals(_ context.Context, resolvedAt *time.Time) ([]store.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Approval, 0)
	for i := range f.approvals {
		row := &f.approvals[i]
		if row.State == "pending" && !row.ExpiresAt.After(*resolvedAt) {
			row.State = "expired"
			row.ResolvedAt = resolvedAt
			out = append(out, *row)
		}
	}
	return out, nil
}

func (f *fakeStore) CountPendingApprovalsByRoom(_ context.Context, expiresAt time.Time) ([]store.CountPendingApprovalsByRoomRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := map[uuid.UUID]int64{}
	for _, row := range f.approvals {
		if row.State == "pending" && row.ExpiresAt.After(expiresAt) {
			counts[row.RoomID]++
		}
	}
	out := make([]store.CountPendingApprovalsByRoomRow, 0, len(counts))
	for roomID, count := range counts {
		out = append(out, store.CountPendingApprovalsByRoomRow{RoomID: roomID, Count: count})
	}
	return out, nil
}

func (f *fakeStore) CreateReminder(_ context.Context, arg store.CreateReminderParams) (store.Reminder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := store.Reminder{
		ID:        arg.ID,
		RoomID:    arg.RoomID,
		Text:      arg.Text,
		DueAt:     arg.DueAt,
		State:     "pending",
		CreatedAt: time.Now(),
	}
	f.reminders = append(f.reminders, row)
	return row, nil
}

func (f *fakeStore) ListPendingReminders(_ context.Context, roomID uuid.UUID) ([]store.Reminder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Reminder, 0)
	for _, row := range f.reminders {
		if row.RoomID == roomID && row.State == "pending" {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) CancelReminder(_ context.Context, arg store.CancelReminderParams) (store.Reminder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.reminders {
		row := &f.reminders[i]
		if row.ID == arg.ID && row.RoomID == arg.RoomID && row.State == "pending" {
			row.State = "cancelled"
			return *row, nil
		}
	}
	return store.Reminder{}, pgx.ErrNoRows
}

func (f *fakeStore) ListDueReminders(_ context.Context, arg store.ListDueRemindersParams) ([]store.Reminder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Reminder, 0)
	for _, row := range f.reminders {
		if row.State != "pending" || row.DueAt.After(arg.DueAt) {
			continue
		}
		out = append(out, row)
		if len(out) >= int(arg.RowLimit) {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) FireReminder(_ context.Context, arg store.FireReminderParams) (store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var due store.Reminder
	found := false
	for i := range f.reminders {
		row := &f.reminders[i]
		if row.ID == arg.ReminderID && row.State == "pending" {
			row.State = "fired"
			row.FiredAt = arg.FiredAt
			due = *row
			found = true
			break
		}
	}
	if !found {
		return store.Message{}, pgx.ErrNoRows
	}
	created := time.Now()
	if arg.FiredAt != nil {
		created = *arg.FiredAt
	}
	message := store.Message{
		ID:        arg.MessageID,
		RoomID:    due.RoomID,
		Role:      "system",
		Body:      arg.Body,
		Meta:      []byte("{}"),
		CreatedAt: created,
	}
	f.messages = append(f.messages, message)
	return message, nil
}

func (f *fakeStore) ListPushSubscriptions(context.Context) ([]store.PushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.PushSubscription(nil), f.pushSubs...), nil
}

func (f *fakeStore) UpsertPushSubscription(_ context.Context, arg store.UpsertPushSubscriptionParams) (store.PushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.pushSubs {
		if f.pushSubs[i].Endpoint == arg.Endpoint {
			f.pushSubs[i].RoomID = arg.RoomID
			f.pushSubs[i].P256dh = arg.P256dh
			f.pushSubs[i].Auth = arg.Auth
			f.pushSubs[i].UpdatedAt = time.Now()
			return f.pushSubs[i], nil
		}
	}
	now := time.Now()
	row := store.PushSubscription{
		ID: arg.ID, RoomID: arg.RoomID, Endpoint: arg.Endpoint,
		P256dh: arg.P256dh, Auth: arg.Auth, CreatedAt: now, UpdatedAt: now,
	}
	f.pushSubs = append(f.pushSubs, row)
	return row, nil
}

func (f *fakeStore) DeletePushSubscription(_ context.Context, endpoint string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.pushSubs[:0]
	var removed int64
	for _, row := range f.pushSubs {
		if row.Endpoint == endpoint {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	f.pushSubs = kept
	return removed, nil
}

func (f *fakeStore) approvalsFor(roomID uuid.UUID) []store.Approval {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Approval, 0)
	for _, row := range f.approvals {
		if row.RoomID == roomID {
			out = append(out, row)
		}
	}
	return out
}

func (f *fakeStore) messagesFor(roomID uuid.UUID) []store.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Message, 0)
	for _, row := range f.messages {
		if row.RoomID == roomID {
			out = append(out, row)
		}
	}
	return out
}

type fakeHermes struct {
	mu sync.Mutex

	createID     string
	createErr    error
	createCalls  int
	createPrompt string

	deleteCalls []string
	deleteErr   error

	events       []hermes.Event
	streamErr    error
	streamCalls  int
	lastText     string
	instructions string
	onStream     func(ctx context.Context, emit func(hermes.Event) error) error

	resolveCalls []resolveCall
	resolveErr   error
	stopCalls    []string
	stopErr      error
	stopStatus   string
}

type resolveCall struct {
	RunID     string
	RequestID string
	Choice    string
}

func (f *fakeHermes) CreateSession(_ context.Context, _ string, systemPrompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.createPrompt = systemPrompt
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.createID, nil
}

func (f *fakeHermes) DeleteSession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, sessionID)
	return f.deleteErr
}

func (f *fakeHermes) StreamChat(ctx context.Context, _ string, text, instructions string, onEvent func(hermes.Event) error) error {
	f.mu.Lock()
	f.streamCalls++
	f.lastText = text
	f.instructions = instructions
	f.mu.Unlock()
	if f.onStream != nil {
		return f.onStream(ctx, onEvent)
	}
	for _, ev := range f.events {
		if err := onEvent(ev); err != nil {
			return err
		}
	}
	return f.streamErr
}

func (f *fakeHermes) ResolveApproval(_ context.Context, runID, requestID, choice string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveCalls = append(f.resolveCalls, resolveCall{RunID: runID, RequestID: requestID, Choice: choice})
	return f.resolveErr
}

func (f *fakeHermes) StopRun(_ context.Context, runID string) (hermes.RunStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls = append(f.stopCalls, runID)
	if f.stopErr != nil {
		return hermes.RunStatus{}, f.stopErr
	}
	status := f.stopStatus
	if status == "" {
		status = "stopping"
	}
	return hermes.RunStatus{RunID: runID, Status: status}, nil
}

func newTestApp(t *testing.T, st *fakeStore, hm hermes.Client) *echo.Echo {
	t.Helper()
	logger := testLogger()
	authService := auth.New(st, testPassphrase, false, logger)
	return New(Options{
		Store:   st,
		Hermes:  hm,
		Auth:    authService,
		Logger:  logger,
		Version: "test",
		DataDir: t.TempDir(),
		WebDir:  t.TempDir(),
	})
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func request(t *testing.T, e *echo.Echo, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, e *echo.Echo) *http.Cookie {
	t.Helper()
	rec := request(t, e, http.MethodPost, "/api/auth/login", map[string]string{"passphrase": testPassphrase}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	for _, c := range cookies {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatalf("login did not set %s", auth.CookieName)
	return nil
}

func createRoom(t *testing.T, e *echo.Echo, cookie *http.Cookie, hm *fakeHermes, st *fakeStore) store.Room {
	t.Helper()
	hm.createID = "api_test_room"
	rec := request(t, e, http.MethodPost, "/api/rooms", map[string]string{"name": "Work", "topic": "ship"}, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create room status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(st.rooms) != 1 {
		t.Fatalf("rooms stored = %d, want 1", len(st.rooms))
	}
	for _, row := range st.rooms {
		return row
	}
	return store.Room{}
}

type sseEvent struct {
	Name string
	Data string
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	var name string
	var data []string
	flush := func() {
		if name == "" && len(data) == 0 {
			return
		}
		out = append(out, sseEvent{Name: name, Data: strings.Join(data, "\n")})
		name = ""
		data = nil
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
	return out
}

func TestLoginWrongPassphraseIs401(t *testing.T) {
	st := newFakeStore()
	e := newTestApp(t, st, &fakeHermes{})
	rec := request(t, e, http.MethodPost, "/api/auth/login", map[string]string{"passphrase": "wrong"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] == "" {
		t.Fatal("expected a human error message")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("wrong passphrase must not set a cookie")
	}
}

func TestLoginSetsHttpOnlyCookieAndProtectsRoutes(t *testing.T) {
	st := newFakeStore()
	e := newTestApp(t, st, &fakeHermes{})

	rec := request(t, e, http.MethodPost, "/api/auth/login", map[string]string{"passphrase": testPassphrase}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login did not set iris_session")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}

	rec = request(t, e, http.MethodGet, "/api/rooms", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("protected route without cookie = %d, want 401", rec.Code)
	}

	rec = request(t, e, http.MethodGet, "/api/health", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("health without cookie = %d, want 200", rec.Code)
	}
	var health map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health["ok"] != true || health["version"] != "test" {
		t.Fatalf("health = %v", health)
	}

	rec = request(t, e, http.MethodGet, "/api/auth/me", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("me with cookie = %d, want 200", rec.Code)
	}

	rec = request(t, e, http.MethodPost, "/api/auth/logout", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", rec.Code)
	}
	rec = request(t, e, http.MethodGet, "/api/rooms", nil, cookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("route after logout = %d, want 401", rec.Code)
	}
}

func TestCreateRoomCreatesHermesSessionFirst(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_abc"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)

	rec := request(t, e, http.MethodPost, "/api/rooms", map[string]string{"name": "Work", "topic": "ship"}, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if hm.createCalls != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", hm.createCalls)
	}
	if st.createRoomCalls != 1 {
		t.Fatalf("CreateRoom calls = %d, want 1", st.createRoomCalls)
	}
	if st.lastRoomArg.HermesSessionID != "api_abc" {
		t.Fatalf("stored hermes session = %q, want api_abc", st.lastRoomArg.HermesSessionID)
	}
}

func TestCreateRoomHermesFailureLeavesNoRow(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createErr: errors.New("connection refused")}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)

	rec := request(t, e, http.MethodPost, "/api/rooms", map[string]string{"name": "Work"}, cookie)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	if len(st.rooms) != 0 || st.createRoomCalls != 0 {
		t.Fatalf("row was written after Hermes failed: rooms=%d calls=%d", len(st.rooms), st.createRoomCalls)
	}
	if !strings.Contains(rec.Body.String(), "Tidak ada room") {
		t.Fatalf("error copy is not human: %s", rec.Body.String())
	}
}

func TestCreateRoomRowFailureCleansUpHermesSession(t *testing.T) {
	st := newFakeStore()
	st.failCreateRoom = errors.New("db is down")
	hm := &fakeHermes{createID: "api_orphan"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)

	rec := request(t, e, http.MethodPost, "/api/rooms", map[string]string{"name": "Work"}, cookie)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if len(hm.deleteCalls) != 1 || hm.deleteCalls[0] != "api_orphan" {
		t.Fatalf("orphan cleanup = %v, want [api_orphan]", hm.deleteCalls)
	}
}

func TestMessageStreamForwardsDeltasAndPersistsAssistant(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{createID: "api_stream"}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	hm.events = []hermes.Event{
		{Kind: "started", RunID: "run_1", MessageID: "msg_1"},
		{Kind: "delta", Text: "Hel", RunID: "run_1", MessageID: "msg_1"},
		{Kind: "tool", Name: "terminal", Status: "running", RunID: "run_1"},
		{Kind: "delta", Text: "lo", RunID: "run_1", MessageID: "msg_1"},
		{Kind: "done", Text: "Hello", RunID: "run_1", MessageID: "msg_1"},
	}

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "hi"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(echo.HeaderContentType); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}

	events := parseSSE(t, rec.Body.String())
	var deltas []string
	var sawTool, sawDone bool
	for _, ev := range events {
		switch ev.Name {
		case "delta":
			var payload struct{ Text string }
			if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
				t.Fatalf("delta payload: %v", err)
			}
			deltas = append(deltas, payload.Text)
		case "tool":
			sawTool = true
		case "done":
			sawDone = true
		}
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("deltas = %v, want Hel then lo", deltas)
	}
	if !sawTool {
		t.Error("tool event was not forwarded")
	}
	if !sawDone {
		t.Error("done event was not forwarded")
	}

	var rows []store.Message
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "assistant" {
			rows = append(rows, row)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want 1", len(rows))
	}
	if rows[0].Body != "Hello" {
		t.Fatalf("assistant body = %q, want Hello", rows[0].Body)
	}
	if rows[0].RunID == nil || *rows[0].RunID != "run_1" {
		t.Fatalf("assistant run_id = %v, want run_1", rows[0].RunID)
	}

	userRows := 0
	for _, row := range st.messagesFor(room.ID) {
		if row.Role == "user" && row.Body == "hi" {
			userRows++
		}
	}
	if userRows != 1 {
		t.Fatalf("user rows = %d, want 1", userRows)
	}
}

func TestClientDisconnectDoesNotStopTheTurn(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	firstSent := make(chan struct{})
	continueTurn := make(chan struct{})
	hm.onStream = func(ctx context.Context, emit func(hermes.Event) error) error {
		if err := emit(hermes.Event{Kind: "delta", Text: "part one ", RunID: "run_2"}); err != nil {
			return err
		}
		close(firstSent)
		<-continueTurn
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := emit(hermes.Event{Kind: "delta", Text: "part two", RunID: "run_2"}); err != nil {
			return err
		}
		return nil
	}

	reqCtx, cancelReq := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", strings.NewReader(`{"text":"long job"}`))
	req = req.WithContext(reqCtx)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		e.ServeHTTP(rec, req)
		close(done)
	}()

	<-firstSent
	cancelReq() // the phone went away
	close(continueTurn)
	<-done

	rows := st.messagesFor(room.ID)
	var assistant string
	for _, row := range rows {
		if row.Role == "assistant" {
			assistant = row.Body
		}
	}
	if assistant != "part one part two" {
		t.Fatalf("assistant body after disconnect = %q, want the full turn", assistant)
	}
}

func TestUnknownHermesEventIsForwardedNotFatal(t *testing.T) {
	st := newFakeStore()
	hm := &fakeHermes{}
	e := newTestApp(t, st, hm)
	cookie := login(t, e)
	room := createRoom(t, e, cookie, hm, st)

	hm.events = []hermes.Event{
		{Kind: "unknown", OriginalKind: "future.event", Raw: []byte(`{"x":1}`)},
		{Kind: "delta", Text: "still here"},
	}

	rec := request(t, e, http.MethodPost, "/api/rooms/"+room.ID.String()+"/messages", map[string]string{"text": "go"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	events := parseSSE(t, rec.Body.String())
	var sawUnknown bool
	for _, ev := range events {
		if ev.Name == "unknown" && strings.Contains(ev.Data, "future.event") {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatalf("unknown event not forwarded: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "still here") {
		t.Fatalf("turn stopped after unknown event: %s", rec.Body.String())
	}
}
