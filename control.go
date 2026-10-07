package coord

import (
	"coord/fsm"
	"crypto/subtle"
	"encoding/json"
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

// ControlHandler exposes authenticated supervisor operations. Use TLS outside localhost.
func (c *Coordinator) ControlHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
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
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		d.DisallowUnknownFields()
		if err := d.Decode(&req); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		var trailing any
		if err := d.Decode(&trailing); err != io.EOF {
			http.Error(w, "invalid trailing JSON", http.StatusBadRequest)
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
