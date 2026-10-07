package coord

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestDocumentedInvariantsAreDefinedOnce is the DP-004 mechanism: an Accepted
// document may reference an invariant that another document defines. A renamed,
// removed or duplicated identifier is invisible to prose review, so this test
// makes it a build failure.
//
// Форматы определений, найденные во всех ADR на момент написания теста:
//   - «### ID — Заголовок» в ADR-0004 (RT-01…RT-06) и ADR-0005 (ST-01…ST-07);
//   - «- **ID — название.** текст» в ADR-0002 (DG-01…), ADR-0003 (EXT-01…,
//     TID-01, TID-01a) и в ADR-0005 для ссылочной строки;
//   - суффикс строчной буквой используется для добавленного инварианта (TID-01a);
//   - «- **ID — название.** текст» в ADR-0007 (BR-01…) и ADR-0008 (AC-01…).
//
// Определение считается здесь только заголовком раздела или пунктом списка, а не
// любым упоминанием: иначе каждая ссылка стала бы «вторым определением».
var (
	invariantID = regexp.MustCompile(`^(?:### |- \*\*)((?:DG|EXT|TID|RT|ST|BR|AC)-\d+[a-z]?)`)
	mention     = regexp.MustCompile(`\b(?:DG|EXT|TID|RT|ST|BR|AC)-\d+[a-z]?\b`)
	// minDefinitions — фактическое число определений на момент написания теста.
	// Пустой или почти пустой парсер прошёл бы проверку ссылок, поэтому тест
	// сначала убеждается, что нашёл достаточно.
	minDefinitions = 60
)

// documentPaths walks docs recursively: a plan that quotes an invariant tomorrow
// must be covered today, or the check simply does not see where the defect can
// appear (DP-001).
func documentPaths(t *testing.T) []string {
	t.Helper()
	paths := []string{"README.md", "CHANGELOG.md"}
	err := filepath.WalkDir("docs", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == ".md" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

// definedInvariants returns every identifier that has exactly one definition.
func definedInvariants(t *testing.T, paths []string) map[string][]string {
	t.Helper()
	defined := map[string][]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(data), "\n") {
			if m := invariantID.FindStringSubmatch(line); m != nil {
				defined[m[1]] = append(defined[m[1]], path+":"+strconv.Itoa(n+1))
			}
		}
	}
	return defined
}

func TestDocumentedInvariantsAreDefinedOnce(t *testing.T) {
	paths := documentPaths(t)
	defined := definedInvariants(t, paths)

	if len(defined) < minDefinitions {
		t.Fatalf("найдено %d определений инвариантов, ожидалось не меньше %d: парсер, вероятно, сломан",
			len(defined), minDefinitions)
	}

	for id, where := range defined {
		if len(where) > 1 {
			sort.Strings(where)
			t.Errorf("%s определён %d раз: %s — дубликат определения (DP-003)", id, len(where), strings.Join(where, ", "))
		}
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(data), "\n") {
			if invariantID.FindStringSubmatch(line) != nil {
				continue
			}
			for _, m := range mention.FindAllString(line, -1) {
				if _, ok := defined[m]; !ok {
					t.Errorf("висячая ссылка: %s:%d упоминает %s, но ни один документ его не определяет",
						path, n+1, m)
				}
			}
		}
	}
}
