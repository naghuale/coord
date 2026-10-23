//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installTelepathy writes <home>/<root...>/<version>/dist/server.mjs with body.
func installTelepathy(t *testing.T, home string, root []string, version, body string) string {
	t.Helper()
	dir := filepath.Join(append(append([]string{home}, root...), version, "dist")...)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "server.mjs")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func orphan(t *testing.T, server string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(server)), ".orphaned_at"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
}

var claudeRoot, codexRoot = telepathyCacheRoots[0], telepathyCacheRoots[1]

func TestDiscoverNoInstallationFails(t *testing.T) {
	home := t.TempDir()
	if _, err := discover("", "", home); err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Fatalf("err=%v", err)
	}
}

func TestDiscoverSingleInstallationSelected(t *testing.T) {
	home := t.TempDir()
	want := installTelepathy(t, home, codexRoot, "0.9.0", "v090")
	sel, err := discover("", "", home)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := fileDigest(want)
	if sel.Path != want || sel.Source != "discovery" || sel.Digest != digest {
		t.Fatalf("sel=%+v", sel)
	}
}

func TestDiscoverIdenticalCopiesAcrossHostsAreOneArtifact(t *testing.T) {
	home := t.TempDir()
	first := installTelepathy(t, home, claudeRoot, "0.9.0", "v090")
	installTelepathy(t, home, codexRoot, "0.9.0", "v090")
	sel, err := discover("", "", home)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Path != first || len(sel.Candidates) != 2 {
		t.Fatalf("sel=%+v", sel)
	}
}

func TestDiscoverSeveralVersionsAreAmbiguousNotNewest(t *testing.T) {
	home := t.TempDir()
	installTelepathy(t, home, claudeRoot, "0.8.0", "v080")
	installTelepathy(t, home, codexRoot, "0.9.0", "v090")
	sel, err := discover("", "", home)
	if err == nil || !strings.HasPrefix(err.Error(), "AMBIGUOUS_TELEPATHY_INSTALLATION") {
		t.Fatalf("err=%v", err)
	}
	if sel.Path != "" || len(sel.Candidates) != 2 {
		t.Fatalf("selected despite ambiguity: %+v", sel)
	}
}

func TestDiscoverSameVersionDifferentBytesAmbiguous(t *testing.T) {
	home := t.TempDir()
	installTelepathy(t, home, claudeRoot, "0.9.0", "build-a")
	installTelepathy(t, home, codexRoot, "0.9.0", "build-b")
	if _, err := discover("", "", home); err == nil || !strings.HasPrefix(err.Error(), "AMBIGUOUS_TELEPATHY_INSTALLATION") {
		t.Fatalf("err=%v", err)
	}
}

func TestDiscoverExcludesOrphanedAndUnsupportedCopies(t *testing.T) {
	home := t.TempDir()
	orphan(t, installTelepathy(t, home, claudeRoot, "0.8.0", "v080"))
	installTelepathy(t, home, claudeRoot, "0.10.0", "v0100")
	want := installTelepathy(t, home, claudeRoot, "0.9.0", "v090")
	sel, err := discover("", "", home)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Path != want {
		t.Fatalf("sel=%+v", sel)
	}
	reasons := map[string]string{}
	for _, c := range sel.Candidates {
		reasons[c.Version] = c.Excluded
	}
	if reasons["0.8.0"] != "orphaned" || reasons["0.10.0"] != "unsupported version" || reasons["0.9.0"] != "" {
		t.Fatalf("reasons=%v", reasons)
	}
}

func TestDiscoverOnlyUnsupportedFails(t *testing.T) {
	home := t.TempDir()
	installTelepathy(t, home, codexRoot, "0.10.0", "v0100")
	if _, err := discover("", "", home); err == nil {
		t.Fatal("unsupported-only installation selected")
	}
}

func TestDiscoverExplicitAndEnvWinOverScan(t *testing.T) {
	home := t.TempDir()
	installTelepathy(t, home, claudeRoot, "0.8.0", "v080")
	installTelepathy(t, home, codexRoot, "0.9.0", "v090")
	explicit := filepath.Join(t.TempDir(), "server.mjs")
	if err := os.WriteFile(explicit, []byte("explicit"), 0o600); err != nil {
		t.Fatal(err)
	}
	sel, err := discover(explicit, "/ignored/env/server.mjs", home)
	if err != nil || sel.Path != explicit || sel.Source != "--server" || len(sel.Candidates) != 0 {
		t.Fatalf("sel=%+v err=%v", sel, err)
	}
	sel, err = discover("", explicit, home)
	if err != nil || sel.Path != explicit || sel.Source != "TELEPATHY_SERVER" {
		t.Fatalf("sel=%+v err=%v", sel, err)
	}
	if _, err = discover(filepath.Join(t.TempDir(), "missing.mjs"), "", home); err == nil {
		t.Fatal("missing explicit path accepted")
	}
}

func TestReportTelepathySelectionListsCandidatesAndChoice(t *testing.T) {
	home := t.TempDir()
	orphan(t, installTelepathy(t, home, claudeRoot, "0.8.0", "v080"))
	want := installTelepathy(t, home, codexRoot, "0.9.0", "v090")
	sel, err := discover("", "", home)
	var out bytes.Buffer
	reportTelepathySelection(&out, sel, err)
	text := out.String()
	for _, line := range []string{"version=0.8.0", "excluded: orphaned", "version=0.9.0 sha256=" + sel.Digest + " eligible", "telepathy selected path=" + want + " source=discovery sha256=" + sel.Digest} {
		if !strings.Contains(text, line) {
			t.Fatalf("missing %q in:\n%s", line, text)
		}
	}
}
