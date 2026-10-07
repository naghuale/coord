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
	invariantID = regexp.MustCompile(`^(?:### |- \*\*)((?:DG|EXT|TID|RT|ST|BR|AC|CM|TM)-\d+[a-z]?)`)
	mention     = regexp.MustCompile(`\b(?:DG|EXT|TID|RT|ST|BR|AC|CM|TM)-\d+[a-z]?\b`)
	// minDefinitions — фактическое число определений на момент написания теста.
	// Пустой или почти пустой парсер прошёл бы проверку ссылок, поэтому тест
	// сначала убеждается, что нашёл достаточно.
	// Предел: переименование определения без ссылок, например TM-01→TM-26, не ловится ни счётчиком, ни ссылками; частично закрывается ТЗ-18.
	minDefinitions = 100
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

// Документные идентификаторы, не являющиеся инвариантами. ADR-NNNN — это
// номер решения, P0-N — номер этапа плана. Для них требуется другое: номер
// должен существовать либо как файл, либо как запись в таблице «Номера ADR»
// (резервы), либо как строка таблицы этапов в ROADMAP.
var (
	adrMention = regexp.MustCompile(`ADR-\d{4}`)
	adrFile    = regexp.MustCompile(`^(ADR-\d{4})-.+\.md$`)
	stageRow   = regexp.MustCompile(`^\|\s*(P0-\d)\s*\|`)
)

// adrCatalog возвращает файлы ADR по номеру и номера, занятые резервом.
func adrCatalog(t *testing.T) (map[string]string, map[string]bool) {
	t.Helper()
	files := map[string]string{}
	entries, err := os.ReadDir("docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if m := adrFile.FindStringSubmatch(entry.Name()); m != nil {
			files[m[1]] = entry.Name()
		}
	}
	reserved := map[string]bool{}
	target := filepath.Join("docs", "TARGET-ARCHITECTURE.md")
	data, err := os.ReadFile(target)
	if err != nil {
		return files, reserved
	}
	inTable := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## ") {
			inTable = strings.HasPrefix(line, "## Номера ADR")
			continue
		}
		if inTable {
			// Только первая колонка объявляет зарезервированный номер; третья
			// цитирует ADR, где резерв объявлен, и не является объявлением.
			cells := strings.Split(line, "|")
			if len(cells) < 3 {
				continue
			}
			for _, m := range adrMention.FindAllString(cells[1], -1) {
				reserved[m] = true
			}
		}
	}
	return files, reserved
}

// roadmapStages возвращает номера этапов, объявленные таблицей в ROADMAP.
func roadmapStages(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("docs", "ROADMAP.md"))
	if err != nil {
		t.Fatal(err)
	}
	stages := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if m := stageRow.FindStringSubmatch(line); m != nil {
			stages[m[1]] = true
		}
	}
	return stages
}

// TestDocumentedIdentifiersResolve проверяет реестр номеров: каждое упоминание
// ADR-NNNN указывает на существующий файл или на зарезервированный номер, на один
// номер приходится не больше одного файла, а зарезервированный номер не может
// быть уже занят файлом. Плюс каждое упоминание этапа P0-N объявлено в ROADMAP.
//
// Известное ограничение: правило «называй номер вместе с названием этапа» этой
// проверкой не подтверждается. Тест видит только существование строки этапа в
// ROADMAP, но не её содержимое, поэтому перенумерация P0-x с молчаливым
// сохранением номера останется незамеченной. Проверка содержания — задача ревью.
func TestDocumentedIdentifiersResolve(t *testing.T) {
	files, reserved := adrCatalog(t)
	if len(files) < 7 {
		t.Fatalf("найдено %d файлов ADR, ожидалось не меньше 7: парсер, вероятно, сломан", len(files))
	}
	if len(reserved) < 1 {
		t.Fatalf("найдено %d зарезервированных номеров ADR, ожидался хотя бы один: таблица «Номера ADR» не прочитана", len(reserved))
	}
	for number := range reserved {
		if name, taken := files[number]; taken {
			t.Errorf("номер %s одновременно зарезервирован и занят файлом %s: резерв нужно снять", number, name)
		}
	}
	stages := roadmapStages(t)
	for _, path := range documentPaths(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(data), "\n") {
			for _, m := range adrMention.FindAllString(line, -1) {
				if _, ok := files[m]; ok {
					continue
				}
				if reserved[m] {
					continue
				}
				t.Errorf("ссылка на несуществующий ADR: %s:%d упоминает %s, нет ни файла, ни записи в таблице «Номера ADR»",
					path, n+1, m)
			}
			for _, m := range regexp.MustCompile(`P0-\d`).FindAllString(line, -1) {
				if !stages[m] {
					t.Errorf("ссылка на несуществующий этап: %s:%d упоминает %s, нет строки этапа в docs/ROADMAP.md",
						path, n+1, m)
				}
			}
		}
	}
}

// verificationLine is the ACP-04 annotation every rule definition carries. A rule
// is either verified by named tests, partially verified with the uncovered part
// stated, or explicitly not implemented; silence is not an allowed state.
var (
	verificationLine = regexp.MustCompile(`^\s*Проверка: (.+)$`)
	testReference    = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
	testFunc         = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
)

// ruleBlock returns the lines that belong to the rule defined at lines[start]:
// a list item continues on indented lines (blank lines included while the next
// line stays indented); a heading section runs until the next heading.
func ruleBlock(lines []string, start int) []string {
	heading := strings.HasPrefix(lines[start], "### ")
	end := start + 1
	for ; end < len(lines); end++ {
		line := lines[end]
		if heading {
			if strings.HasPrefix(line, "#") {
				break
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			if end+1 < len(lines) && strings.HasPrefix(lines[end+1], "  ") {
				continue
			}
			break
		}
		if !strings.HasPrefix(line, "  ") {
			break
		}
	}
	return lines[start:end]
}

// declaredTests indexes every Test function in the module's test files.
func declaredTests(t *testing.T) map[string]bool {
	t.Helper()
	tests := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".scratch") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range testFunc.FindAllStringSubmatch(string(data), -1) {
			tests[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tests
}

// checkVerification classifies one annotation value. Allowed forms:
//
//	`TestA`, `TestB`                                  verified
//	частично — `TestA`; не реализовано: <что именно>   partial
//	не реализовано                                     not implemented
//
// A partial annotation must name the uncovered part, otherwise a partial test
// would read as full coverage (DP-001).
func checkVerification(value string, tests map[string]bool) (string, []string) {
	var problems []string
	refs := testReference.FindAllStringSubmatch(value, -1)
	for _, ref := range refs {
		if !tests[ref[1]] {
			problems = append(problems, "тест "+ref[1]+" не найден ни в одном *_test.go")
		}
	}
	switch {
	case value == "не реализовано":
		return "not", problems
	case strings.HasPrefix(value, "частично — "):
		if len(refs) == 0 {
			problems = append(problems, "частичная проверка без имени теста")
		}
		if !strings.Contains(value, "; не реализовано: ") || strings.HasSuffix(strings.TrimSpace(value), "не реализовано:") {
			problems = append(problems, "частичная проверка не называет непокрытую часть")
		}
		return "partial", problems
	default:
		if len(refs) == 0 {
			problems = append(problems, "неизвестная форма аннотации: "+value)
		}
		if rest := strings.NewReplacer(",", "", " ", "").Replace(testReference.ReplaceAllString(value, "")); len(refs) > 0 && rest != "" {
			problems = append(problems, "в проверенной аннотации допустимы только имена тестов через запятую: "+value)
		}
		return "verified", problems
	}
}

// TestRuleVerificationAnnotations is the ACP-04 mechanism: every rule definition
// states which tests verify it, or that it is not implemented, and the named tests
// exist. The coverage summary is computed here rather than kept in a document.
func TestRuleVerificationAnnotations(t *testing.T) {
	tests := declaredTests(t)
	type counts struct{ total, verified, partial, not int }
	byPrefix := map[string]*counts{}
	seen := 0
	for _, path := range documentPaths(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for n, line := range lines {
			m := invariantID.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			seen++
			id := m[1]
			prefix := id[:strings.Index(id, "-")]
			if byPrefix[prefix] == nil {
				byPrefix[prefix] = &counts{}
			}
			c := byPrefix[prefix]
			c.total++
			var values []string
			for _, l := range ruleBlock(lines, n) {
				if v := verificationLine.FindStringSubmatch(l); v != nil {
					values = append(values, strings.TrimSpace(v[1]))
				}
			}
			where := path + ":" + strconv.Itoa(n+1)
			if len(values) != 1 {
				t.Errorf("%s: у правила %s %d строк «Проверка: …», нужна ровно одна", where, id, len(values))
				continue
			}
			kind, problems := checkVerification(values[0], tests)
			for _, p := range problems {
				t.Errorf("%s: %s: %s", where, id, p)
			}
			switch kind {
			case "verified":
				c.verified++
			case "partial":
				c.partial++
			default:
				c.not++
			}
		}
	}
	if seen < minDefinitions {
		t.Fatalf("найдено %d определений правил, ожидалось не меньше %d: разбор сломан", seen, minDefinitions)
	}
	prefixes := make([]string, 0, len(byPrefix))
	for p := range byPrefix {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	for _, p := range prefixes {
		c := byPrefix[p]
		t.Logf("%-4s всего %3d  проверено %3d  частично %3d  не реализовано %3d", p, c.total, c.verified, c.partial, c.not)
	}
}

func TestVerificationAnnotationRejectsUnsupportedForms(t *testing.T) {
	tests := map[string]bool{"TestReal": true}
	for _, value := range []string{
		"`TestMissing`",
		"частично — `TestReal`",
		"частично — `TestReal`; не реализовано:",
		"частично; не реализовано: всё",
		"проверено вручную",
		"`TestReal` и ещё ревью",
	} {
		if _, problems := checkVerification(value, tests); len(problems) == 0 {
			t.Errorf("аннотация %q принята, хотя должна быть отклонена", value)
		}
	}
	for _, value := range []string{"`TestReal`", "не реализовано", "частично — `TestReal`; не реализовано: Task.Fence"} {
		if _, problems := checkVerification(value, tests); len(problems) != 0 {
			t.Errorf("корректная аннотация %q отклонена: %v", value, problems)
		}
	}
}
