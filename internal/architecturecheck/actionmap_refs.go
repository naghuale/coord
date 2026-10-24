package architecturecheck

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Patterns that count as an Action Map reference hit for NV-4.
var actionMapHitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ACTION-MAP\.md`),
	regexp.MustCompile(`(?i)ACTION-MAP\.yaml`),
	regexp.MustCompile(`(?i)Action Map`),
	regexp.MustCompile(`(?i)action map`),
	regexp.MustCompile(`карта действий`),
	regexp.MustCompile(`(?i)запрещено[^\n]{0,80}Action Map`),
	regexp.MustCompile(`(?i)согласно[^\n]{0,80}Action Map`),
	regexp.MustCompile(`(?i)требует[^\n]{0,80}Action Map`),
	regexp.MustCompile(`(?i)соответствует событию в Action Map`),
	regexp.MustCompile(`(?i)перед переходом прочитать Action Map`),
	regexp.MustCompile(`(?i)обновить Action Map`),
}

// Default scan roots for NV-4 when classifications are present.
var defaultActionMapScanPaths = []string{
	"docs/operations/AGENT-SESSION-RECOVERY-REGULATION.md",
	"docs/operations/HUMAN-ACTION-PLAYBOOK.md",
	"docs/templates/COORDINATOR-RECOVERY-PROMPT.md",
	"docs/templates/RECOVERY-PROMPT-SHORT.md",
	"docs/ARCHITECTURE.md",
	"AGENTS.md",
}

func validateActionMapRefs(root string, reg Registry, actionMap actionMapDocStatus) []Violation {
	if len(reg.ActionMapReferenceClassifications) == 0 {
		return nil
	}
	var vs []Violation
	classified := map[string]ActionMapReferenceClassification{}
	for _, c := range reg.ActionMapReferenceClassifications {
		if !actionMapClasses[c.Class] {
			vs = append(vs, Violation{Code: CodeStatusInvalid, Detail: fmt.Sprintf("unknown action-map classification class %q at %s:%d", c.Class, c.Path, c.Line)})
			continue
		}
		key := fmt.Sprintf("%s:%d", c.Path, c.Line)
		classified[key] = c
		if c.Class == "AUTHORITATIVE_REFERENCE" && actionMap.found && actionMap.proposed {
			vs = append(vs, Violation{Code: CodeAuthoritativeActionMap, Detail: fmt.Sprintf("%s:%d AUTHORITATIVE_REFERENCE while ACTION-MAP.yaml is proposed", c.Path, c.Line)})
		}
	}

	hits := scanActionMapHits(root)
	for _, hit := range hits {
		key := fmt.Sprintf("%s:%d", hit.path, hit.line)
		if _, ok := classified[key]; !ok {
			vs = append(vs, Violation{Code: CodeUnclassifiedActionMap, Detail: fmt.Sprintf("%s:%d unclassified Action Map reference: %s", hit.path, hit.line, truncate(hit.text, 80))})
		}
	}
	return vs
}

type actionMapHit struct {
	path string
	line int
	text string
}

func scanActionMapHits(root string) []actionMapHit {
	var hits []actionMapHit
	for _, rel := range defaultActionMapScanPaths {
		path := filepath.Join(root, rel)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		// Allow long lines in markdown.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			text := sc.Text()
			if matchesActionMapHit(text) {
				hits = append(hits, actionMapHit{path: rel, line: lineNo, text: strings.TrimSpace(text)})
			}
		}
		_ = f.Close()
	}
	return hits
}

func matchesActionMapHit(text string) bool {
	for _, re := range actionMapHitPatterns {
		if re.FindStringIndex(text) != nil {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
