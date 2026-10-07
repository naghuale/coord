package coord

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	changelogTestRef = regexp.MustCompile("`(Test[A-Z][A-Za-z0-9_]*)`")
	goTestDecl       = regexp.MustCompile(`^\s*func\s+(Test[A-Z][A-Za-z0-9_]*)\s*\(`)
)

func changelogCurrentEntries(content string) []string {
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") {
			start = i
			if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "## ")), "Unreleased") {
				break
			}
			// The first dated/versioned section is current when there is no Unreleased section.
			break
		}
	}
	if start < 0 {
		return nil
	}
	var entries []string
	var current strings.Builder
	flush := func() {
		if current.Len() != 0 {
			entries = append(entries, current.String())
			current.Reset()
		}
	}
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "## ") {
			break
		}
		if strings.HasPrefix(line, "- ") {
			flush()
		}
		if current.Len() != 0 {
			current.WriteByte('\n')
		}
		current.WriteString(line)
	}
	flush()
	return entries
}

func changelogTestNames(t *testing.T) map[string]bool {
	t.Helper()
	names := make(map[string]bool)
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".scratch", "bin", "state", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if match := goTestDecl.FindStringSubmatch(line); match != nil {
				names[match[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func validateCurrentChangelog(content string, availableTests map[string]bool) []string {
	entries := changelogCurrentEntries(content)
	var problems []string
	if len(entries) == 0 {
		return []string{"current changelog section has no entries"}
	}
	for _, entry := range entries {
		refs := changelogTestRef.FindAllStringSubmatch(entry, -1)
		if len(refs) == 0 && !strings.Contains(entry, "(не проверено)") && !strings.Contains(entry, "(документация)") {
			problems = append(problems, "entry has no test reference or explicit marker: "+strings.TrimSpace(entry))
		}
	}
	for _, match := range changelogTestRef.FindAllStringSubmatch(content, -1) {
		if !availableTests[match[1]] {
			problems = append(problems, "referenced test does not exist: "+match[1])
		}
	}
	return problems
}

func TestCurrentChangelogEntriesHaveEvidence(t *testing.T) {
	content, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if problems := validateCurrentChangelog(string(content), changelogTestNames(t)); len(problems) != 0 {
		t.Fatalf("CHANGELOG coverage invalid:\n%s", strings.Join(problems, "\n"))
	}
}

func TestChangelogValidatorRejectsMissingEvidence(t *testing.T) {
	problems := validateCurrentChangelog("## 2026-10-08\n\n- Behavior changed.\n", nil)
	if len(problems) == 0 || !strings.Contains(problems[0], "no test reference") {
		t.Fatalf("expected missing-evidence rejection, got %v", problems)
	}
}

func TestChangelogValidatorRejectsUnknownTestReference(t *testing.T) {
	content := "## 2026-10-08\n\n- Behavior changed (`TestDoesNotExist`).\n"
	problems := validateCurrentChangelog(content, map[string]bool{})
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "TestDoesNotExist") {
		t.Fatalf("expected unknown-test rejection, got %v", problems)
	}
}
