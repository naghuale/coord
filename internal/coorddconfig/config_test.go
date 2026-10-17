package coorddconfig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfigFile(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	server := filepath.Join(dir, "server.mjs")
	if err := os.WriteFile(server, []byte("placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	home, presence := filepath.Join(dir, "home"), filepath.Join(dir, "presence")
	for _, path := range []string{home, presence} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile, keyFile := filepath.Join(dir, "control.token"), filepath.Join(dir, "private.key")
	if err := os.WriteFile(tokenFile, []byte("secret-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(private)), 0600); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"schema_version":     1,
		"state_dir":          filepath.Join(dir, "state"),
		"control_addr":       "127.0.0.1:8090",
		"metrics_addr":       "127.0.0.1:9091",
		"control_token_file": tokenFile,
		"transport": map[string]any{
			"kind": "telepathy", "server": server, "home": home,
			"directory": presence, "anchor_pid": 42, "session": "opencode-42",
			"private_key_file": keyFile,
			"peers":            map[string]string{"opencode-43": base64.StdEncoding.EncodeToString(peer)},
		},
	}
	path := filepath.Join(dir, "coordd.json")
	write := func() {
		b, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	return path, filepath.Join(dir, "state")
}

func TestLoadRequiresKnownSchemaVersion(t *testing.T) {
	path, stateDir := validConfigFile(t)
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "missing", data: `{"state_dir":"` + stateDir + `"}`},
		{name: "unknown", data: `{"schema_version":99}`},
		{name: "unknown field", data: strings.TrimSuffix(string(valid), "}") + `,"unexpected":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid config accepted")
			} else if tc.name == "unknown field" && !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("unknown field rejected for another reason: %v", err)
			}
			if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
				t.Fatalf("config validation had filesystem side effect: stat err=%v", err)
			}
		})
	}
}

func TestLoadValidatesCompleteTelepathyConfiguration(t *testing.T) {
	path, _ := validConfigFile(t)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlToken != "secret-token" || len(cfg.PrivateKey) != ed25519.PrivateKeySize || len(cfg.PeerKeys["opencode-43"]) != ed25519.PublicKeySize {
		t.Fatalf("validated secrets/keys not loaded: token=%q private=%d peers=%d", cfg.ControlToken, len(cfg.PrivateKey), len(cfg.PeerKeys))
	}
}

func TestLoadRejectsNonLoopbackAndMissingTransport(t *testing.T) {
	if err := loopbackAddress("0.0.0.0:8000"); err == nil {
		t.Fatal("non-loopback address accepted")
	}
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{SchemaVersion: 1, StateDir: filepath.Join(t.TempDir(), "state"), ControlAddr: "127.0.0.1:8090", MetricsAddr: "127.0.0.1:9091", ControlTokenFile: tokenPath}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "transport.kind") {
		t.Fatalf("missing production transport was not rejected: %v", err)
	}
}

func TestGitHubFixturesAreCredentialFreeReadOnlyRecords(t *testing.T) {
	path, _ := validConfigFile(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["github_observation_fixtures"] = []any{map[string]any{
		"operation": "OBSERVE_PULL_REQUEST", "target": map[string]any{"repository": "naghuale/coord", "pull_request_number": 64, "expected_head": "0123456789012345678901234567890123456789", "expected_state": "OPEN"}, "external_id": "pr-64", "state": "OPEN",
	}}
	encoded, _ := json.Marshal(raw)
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.GitHubObservationFixtures) != 1 || cfg.GitHubObservationFixtures[0].ExternalID != "pr-64" {
		t.Fatalf("fixture not loaded: %+v", cfg.GitHubObservationFixtures)
	}
	raw["github_observation_fixtures"] = []any{map[string]any{"operation": "CREATE_PULL_REQUEST", "target": map[string]any{"repository": "naghuale/coord"}, "external_id": "pr-write", "state": "OPEN", "token": "must-not-be-accepted"}}
	encoded, _ = json.Marshal(raw)
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("credential-bearing write fixture accepted")
	}
}
