package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSandboxFailsClosedWithoutBwrapOrEnv verifies parity with Python
// test_sandbox_fails_closed_without_bwrap_or_env: when the runtime does not
// provide Bubblewrap (non-Linux or bwrap binary absent), Validate must return
// an error rather than silently succeed.
//
// On Linux: tested with a profile that requires bubblewrap and an empty Path.
// On other platforms: the stub always returns an error.
func TestSandboxFailsClosedWithoutBwrapOrEnv(t *testing.T) {
	b := Bubblewrap{Path: ""} // empty path — no bwrap binary
	profile := Profile{
		Name:              "evaluation-linux",
		RequireBubblewrap: true,
		NetworkDeny:       true,
	}
	err := b.Validate(profile)
	if err == nil {
		t.Error("Bubblewrap.Validate must return an error when bwrap is unavailable (fail closed)")
	}
}

// TestSeccompProfileParses verifies parity with Python test_seccomp_profile_parses:
// config/SECCOMP-PROFILES.yaml must parse without error and the python-agent-v1
// profile must deny ptrace explicitly.
func TestSeccompProfileParses(t *testing.T) {
	// Walk up from the test file to find the repo root config/ directory.
	repoRoot := findRepoRoot(t)
	path := filepath.Join(repoRoot, "config", "SECCOMP-PROFILES.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("SECCOMP-PROFILES.yaml not available: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SECCOMP-PROFILES.yaml: %v", err)
	}

	var doc struct {
		Profiles map[string]struct {
			DenyExplicit []string `yaml:"denyExplicit"`
		} `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse SECCOMP-PROFILES.yaml: %v", err)
	}

	profile, ok := doc.Profiles["python-agent-v1"]
	if !ok {
		t.Fatal("profile python-agent-v1 not found in SECCOMP-PROFILES.yaml")
	}
	for _, syscall := range profile.DenyExplicit {
		if syscall == "ptrace" {
			return
		}
	}
	t.Errorf("ptrace not found in python-agent-v1.denyExplicit: %v", profile.DenyExplicit)
}

// TestLibseccompLoaderFailsClosedOnNonLinux verifies that on non-Linux platforms
// (or without CGO/seccomp), the seccomp loader returns an error rather than a
// no-op success. Linux live tests are gated on the linux+cgo+seccomp build tag.
func TestLibseccompLoaderFailsClosedOnNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("live seccomp test: only stub is verified here; Linux live tests have separate build tag")
	}
	var l LibseccompLoader
	err := l.Load(SeccompProfile{Name: "test"})
	if err == nil {
		t.Error("LibseccompLoader.Load must return an error on non-Linux (fail closed)")
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	// Start from this test file's directory and walk up to find go.mod.
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod not found)")
		}
		dir = parent
	}
}
