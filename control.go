package coord

import (
	"coord/fsm"
	"coord/ownercredential"
	"coord/store"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type controlRequest struct {
	Run    string `json:"run"`
	Fence  uint64 `json:"fence"`
	Claim  string `json:"claim"`
	Owner  string `json:"owner"`
	Reason string `json:"reason"`
}

type ownerCommandBoundary interface {
	IssueOwnerCommandChallenge(ownercredential.CommandRequest) (ownercredential.Command, error)
	ApplyOwnerCredentialCommand(string, ownercredential.Command, ownercredential.Assertion) (ownercredential.Receipt, bool, error)
}

type ownerCommandRequest struct {
	ChallengeID string                    `json:"challenge_id"`
	Command     ownercredential.Command   `json:"command"`
	Assertion   ownercredential.Assertion `json:"assertion"`
}

// ControlHandler exposes authenticated supervisor operations. Use TLS outside localhost.
func (c *Coordinator) ControlHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/owner/challenge" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			boundary, ok := c.store.(ownerCommandBoundary)
			if !ok {
				http.NotFound(w, r)
				return
			}
			var request ownercredential.CommandRequest
			if err := decodeControlJSON(w, r, &request, 64<<10); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			command, err := boundary.IssueOwnerCommandChallenge(request)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(command)
			return
		}
		if r.URL.Path == "/owner/command" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			boundary, ok := c.store.(ownerCommandBoundary)
			if !ok {
				http.NotFound(w, r)
				return
			}
			var request ownerCommandRequest
			if err := decodeControlJSON(w, r, &request, 128<<10); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			receipt, replayed, err := boundary.ApplyOwnerCredentialCommand(request.ChallengeID, request.Command, request.Assertion)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(struct {
				Receipt  ownercredential.Receipt `json:"receipt"`
				Replayed bool                    `json:"replayed"`
			}{receipt, replayed})
			return
		}
		if r.URL.Path == "/runs" && r.Method == http.MethodGet {
			out := []fsm.Run{}
			for _, run := range c.store.All() {
				if run.RecoveryNeeded && run.Status == fsm.Stalled {
					out = append(out, run)
				}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req controlRequest
		if err := decodeControlJSON(w, r, &req, 8192); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var err error
		switch r.URL.Path {
		case "/claim":
			err = c.ClaimRecovery(req.Run, req.Fence, req.Claim)
		case "/release":
			err = c.ReleaseRecovery(req.Run, req.Fence, req.Claim)
		case "/transfer":
			err = c.RecoverClaimed(r.Context(), req.Run, req.Owner, req.Fence, req.Claim)
		case "/fail":
			err = c.FailRecovery(req.Run, req.Fence, req.Claim, req.Reason)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "operation refused", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func decodeControlJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return errors.New("invalid trailing JSON")
	}
	return nil
}

var _ ownerCommandBoundary = (*store.CanonicalStore)(nil)
