package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ResolveApproval answers one parked approval. Hermes requires the exact
// request id when the event carried one; the caller must never invent a choice
// the event did not offer.
func (c *APIClient) ResolveApproval(ctx context.Context, runID, requestID, choice string) error {
	payload := map[string]string{"choice": choice}
	// The gateway rejects an explicit empty request_id; only send the field
	// when the approval event actually carried one.
	if requestID != "" {
		payload["request_id"] = requestID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode approval: %w", err)
	}
	endpoint := c.baseURL + "/v1/runs/" + url.PathEscape(runID) + "/approval"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build approval request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("resolve approval: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{StatusCode: resp.StatusCode, Body: string(bytes.TrimSpace(raw))}
	}
	return nil
}

// StopRun asks Hermes to interrupt a live run. A stopped run ends cancelled;
// a run that already finished is reported by the gateway, not invented here.
func (c *APIClient) StopRun(ctx context.Context, runID string) (RunStatus, error) {
	endpoint := c.baseURL + "/v1/runs/" + url.PathEscape(runID) + "/stop"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return RunStatus{}, fmt.Errorf("build stop request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return RunStatus{}, fmt.Errorf("stop run: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return RunStatus{}, fmt.Errorf("read stop response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return RunStatus{}, &APIError{StatusCode: resp.StatusCode, Body: string(bytes.TrimSpace(raw))}
	}
	var status RunStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return RunStatus{}, fmt.Errorf("decode stop response: %w", err)
	}
	return status, nil
}
