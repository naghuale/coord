// Package coorddconfig loads the single schema-versioned production daemon
// configuration. Loading and validation are read-only and precede runtime locks.
package coorddconfig

import (
	"bytes"
	"coord/internal/externalaction"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("invalid coordd configuration")

type InvalidError struct {
	Section string
	Reason  string
}

func (e *InvalidError) Error() string { return e.Reason }
func (e *InvalidError) Unwrap() error { return ErrInvalid }

func FailureDetails(err error) (section, reason string, ok bool) {
	var invalid *InvalidError
	if !errors.As(err, &invalid) {
		return "", "", false
	}
	return invalid.Section, invalid.Reason, true
}

type Config struct {
	SchemaVersion             int                            `json:"schema_version"`
	StateDir                  string                         `json:"state_dir"`
	ControlAddr               string                         `json:"control_addr"`
	MetricsAddr               string                         `json:"metrics_addr"`
	ControlTokenFile          string                         `json:"control_token_file"`
	Transport                 Transport                      `json:"transport"`
	ControlToken              string                         `json:"-"`
	PrivateKey                []byte                         `json:"-"`
	PeerKeys                  map[string][]byte              `json:"-"`
	GitHubObservationFixtures []externalaction.GitHubFixture `json:"github_observation_fixtures,omitempty"`
}

type Transport struct {
	Kind           string            `json:"kind"`
	Server         string            `json:"server"`
	Home           string            `json:"home"`
	Directory      string            `json:"directory"`
	AnchorPID      int               `json:"anchor_pid"`
	Session        string            `json:"session"`
	PrivateKeyFile string            `json:"private_key_file"`
	Peers          map[string]string `json:"peers"`
}

func Load(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, invalid("read config: %v", err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, invalid("decode config: %v", err)
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return cfg, invalid("trailing config JSON")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.SchemaVersion != 1 {
		return invalid("schema_version must be 1")
	}
	if !filepath.IsAbs(c.StateDir) || c.StateDir == "/" {
		return invalid("state_dir must be an absolute non-root path")
	}
	if err := loopbackAddress(c.ControlAddr); err != nil {
		return invalid("control_addr: %v", err)
	}
	if err := loopbackAddress(c.MetricsAddr); err != nil {
		return invalid("metrics_addr: %v", err)
	}
	if c.ControlAddr == c.MetricsAddr && !strings.HasSuffix(c.ControlAddr, ":0") {
		return invalid("control_addr and metrics_addr must differ")
	}
	if !filepath.IsAbs(c.ControlTokenFile) {
		return invalid("control_token_file must be absolute")
	}
	token, err := os.ReadFile(c.ControlTokenFile)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return invalid("control token file must be readable and non-empty")
	}
	c.ControlToken = strings.TrimSpace(string(token))

	t := c.Transport
	if t.Kind == "" {
		return invalidTransport("missing field transport.kind")
	}
	if t.Kind != "telepathy" {
		return invalidTransport("unsupported transport kind %q", t.Kind)
	}
	for _, field := range []struct{ name, path string }{
		{name: "server", path: t.Server},
		{name: "home", path: t.Home},
		{name: "directory", path: t.Directory},
		{name: "private_key_file", path: t.PrivateKeyFile},
	} {
		name, path := field.name, field.path
		if path == "" {
			return invalidTransport("missing field transport.%s", name)
		}
		if !filepath.IsAbs(path) {
			return invalidTransport("transport.%s must be absolute", name)
		}
	}
	if err := requireFile(t.Server, false); err != nil {
		return invalidTransport("transport.server must be an existing readable file: %v", err)
	}
	serverFile, err := os.Open(t.Server)
	if err != nil {
		return invalidTransport("transport.server must be readable: %v", err)
	}
	if err := serverFile.Close(); err != nil {
		return invalidTransport("transport.server could not be closed after validation: %v", err)
	}
	if err := requireFile(t.Home, true); err != nil {
		return invalidTransport("transport.home must be an existing directory: %v", err)
	}
	if err := requireFile(t.Directory, true); err != nil {
		return invalidTransport("transport.directory must be an existing directory: %v", err)
	}
	if t.AnchorPID <= 0 || t.Session != "opencode-"+strconv.Itoa(t.AnchorPID) {
		return invalidTransport("transport.session must match opencode-<anchor_pid>")
	}
	privateText, err := os.ReadFile(t.PrivateKeyFile)
	if err != nil {
		return invalidTransport("transport.private_key_file must be readable")
	}
	private, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(privateText)))
	if err != nil || len(private) != 64 {
		return invalidTransport("transport.private_key_file must contain a base64 Ed25519 private key")
	}
	if len(t.Peers) == 0 {
		return invalidTransport("missing field transport.peers")
	}
	peers := make(map[string][]byte, len(t.Peers))
	for session, encoded := range t.Peers {
		key, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if session == "" || decodeErr != nil || len(key) != 32 {
			return invalidTransport("transport.peers must contain base64 Ed25519 public keys")
		}
		peers[session] = key
	}
	for i, fixture := range c.GitHubObservationFixtures {
		if err := fixture.Validate(); err != nil {
			return invalid("github_observation_fixtures[%d]: %v", i, err)
		}
	}
	c.PrivateKey, c.PeerKeys = private, peers
	return nil
}

func invalid(format string, args ...any) error {
	return &InvalidError{Section: "config", Reason: fmt.Sprintf(format, args...)}
}

func invalidTransport(format string, args ...any) error {
	return &InvalidError{Section: "transport", Reason: fmt.Sprintf(format, args...)}
}

func loopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("must use a numeric loopback address")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("invalid port")
	}
	return nil
}

func requireFile(path string, directory bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() != directory {
		return fmt.Errorf("unexpected file type")
	}
	return nil
}
