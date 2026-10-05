package watchdog

import (
	"bytes"
	"context"
	"coord/fsm"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// HTTPControl connects an external watchdog to the coordinator ControlHandler.
type HTTPControl struct {
	URL, Token, ClaimID string
	Client              *http.Client
}

func (h HTTPControl) request(ctx context.Context, path string, body any) (*http.Response, error) {
	var b bytes.Buffer
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return nil, err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(h.URL, "/")+path, &b)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+h.Token)
	r.Header.Set("Content-Type", "application/json")
	if h.Client == nil {
		return nil, fmt.Errorf("HTTP client required")
	}
	return h.Client.Do(r)
}
func (h HTTPControl) Candidates(ctx context.Context) ([]fsm.Run, error) {
	resp, err := h.request(ctx, "/runs", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("control status %d", resp.StatusCode)
	}
	var runs []fsm.Run
	err = json.NewDecoder(resp.Body).Decode(&runs)
	return runs, err
}
func (h HTTPControl) post(ctx context.Context, path, id string, fence uint64, extra map[string]any) (int, error) {
	if extra == nil {
		extra = map[string]any{}
	}
	extra["run"] = id
	extra["fence"] = fence
	extra["claim"] = h.ClaimID
	resp, err := h.request(ctx, path, extra)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
func (h HTTPControl) Claim(ctx context.Context, id string, f uint64) (bool, error) {
	code, err := h.post(ctx, "/claim", id, f, nil)
	if err != nil {
		return false, err
	}
	if code == 409 {
		return false, nil
	}
	if code != 204 {
		return false, fmt.Errorf("claim status %d", code)
	}
	return true, nil
}
func (h HTTPControl) operation(ctx context.Context, path, id string, f uint64, extra map[string]any) error {
	code, err := h.post(ctx, path, id, f, extra)
	if err != nil {
		return err
	}
	if code != 204 {
		return fmt.Errorf("control %s status %d", path, code)
	}
	return nil
}
func (h HTTPControl) Release(ctx context.Context, id string, f uint64) error {
	return h.operation(ctx, "/release", id, f, nil)
}
func (h HTTPControl) Transfer(ctx context.Context, id, owner string, f uint64) error {
	return h.operation(ctx, "/transfer", id, f, map[string]any{"owner": owner})
}
func (h HTTPControl) Fail(ctx context.Context, id string, f uint64, reason string) error {
	return h.operation(ctx, "/fail", id, f, map[string]any{"reason": reason})
}
