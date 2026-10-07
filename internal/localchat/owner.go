//go:build unix

package localchat

import (
	"coord/internal/atomicfile"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

func loadOwner(state string) (string, error) {
	path := filepath.Join(state, "owner.key")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		key, e := token()
		if e != nil {
			return "", e
		}
		if e = atomicfile.Write(path, []byte(key)); e != nil {
			return "", e
		}
		return key, nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("owner key must be a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("invalid owner key")
	}
	return string(raw), nil
}

// EnableOwner opts a trusted local CLI into administrative access.
// Agents with access to these OS-user files remain inside the same trust boundary.
func (c *Client) EnableOwner(state string) error {
	raw, err := os.ReadFile(filepath.Join(state, "owner.key"))
	if err != nil {
		return err
	}
	if len(raw) != 32 {
		return fmt.Errorf("invalid owner key")
	}
	c.owner = string(raw)
	return nil
}
func (s *Service) isOwner(r *http.Request) bool {
	return s.owner != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Coord-Owner")), []byte(s.owner)) == 1
}
func ownerRequired(path string, input Request) bool {
	switch path {
	case "/health", "/accept", "/reply", "/session-register", "/session-registration", "/session-context", "/session-wait", "/session-say", "/session-ready", "/session-checkpoint", "/session-ack", "/session-regulation":
		return false
	case "/session-resume":
		return input.Resume.Token == "" && input.Resume.Actor == ""
	case "/room-create", "/invite", "/session-assign", "/session-invite", "/room-close":
		return input.Actor == ""
	default:
		return true
	}
}
