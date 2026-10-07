package taskid

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGeneratorProducesCanonicalRoundTrippableID(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	g := newGenerator(func() time.Time { return now }, bytes.NewReader(make([]byte, 10)))

	id, err := g.New()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(id), len(prefix)+26; got != want {
		t.Fatalf("TaskID length = %d, want %d", got, want)
	}
	if !strings.HasPrefix(string(id), prefix) {
		t.Fatalf("TaskID %q does not have prefix %q", id, prefix)
	}
	parsed, err := Parse(id.String())
	if err != nil {
		t.Fatalf("Parse(%q): %v", id, err)
	}
	if parsed != id {
		t.Fatalf("Parse round trip = %q, want %q", parsed, id)
	}
}

func TestEncodeZeroTimestampAndEntropy(t *testing.T) {
	got := prefix + encode(0, [10]byte{})
	want := prefix + strings.Repeat("0", 26)
	if got != want {
		t.Fatalf("zero ULID = %q, want %q", got, want)
	}
}

func TestEncodeMatchesCanonicalULIDExample(t *testing.T) {
	var entropy = [10]byte{0xd6, 0x76, 0x4c, 0x61, 0xef, 0xb9, 0x93, 0x02, 0xbd, 0x5b}
	got := encode(1_469_922_850_259, entropy)
	want := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	if got != want {
		t.Fatalf("ULID = %q, want canonical example %q", got, want)
	}
	id, err := Parse(prefix + want)
	if err != nil {
		t.Fatalf("Parse(canonical ULID example): %v", err)
	}
	gotTime, err := id.Timestamp()
	if err != nil {
		t.Fatalf("Timestamp(): %v", err)
	}
	wantTime := time.UnixMilli(1_469_922_850_259).UTC()
	if !gotTime.Equal(wantTime) {
		t.Fatalf("Timestamp() = %s, want %s", gotTime, wantTime)
	}
	gotTimestamp, gotEntropy, err := decode(want)
	if err != nil {
		t.Fatalf("decode(canonical ULID example): %v", err)
	}
	if gotTimestamp != 1_469_922_850_259 || gotEntropy != entropy {
		t.Fatalf("decode() = (%d, %x), want (%d, %x)", gotTimestamp, gotEntropy, 1_469_922_850_259, entropy)
	}
}

func TestGeneratorIsMonotonicForRepeatedAndBackwardClock(t *testing.T) {
	times := []time.Time{
		time.UnixMilli(1_800_000_000_000),
		time.UnixMilli(1_800_000_000_000),
		time.UnixMilli(1_799_999_999_999),
		time.UnixMilli(1_800_000_000_001),
	}
	index := 0
	g := newGenerator(func() time.Time {
		current := times[index]
		index++
		return current
	}, bytes.NewReader(make([]byte, 20)))

	var previous ID
	for range times {
		current, err := g.New()
		if err != nil {
			t.Fatal(err)
		}
		if previous != "" && current <= previous {
			t.Fatalf("TaskID order regressed: %q <= %q", current, previous)
		}
		previous = current
	}
}

func TestLexicalOrderFollowsEncodedTimestampAcrossGenerators(t *testing.T) {
	entropy := make([]byte, 10)
	older := newGenerator(func() time.Time { return time.UnixMilli(1_800_000_000_000) }, bytes.NewReader(entropy))
	newer := newGenerator(func() time.Time { return time.UnixMilli(1_800_000_000_001) }, bytes.NewReader(entropy))

	olderID, err := older.New()
	if err != nil {
		t.Fatal(err)
	}
	newerID, err := newer.New()
	if err != nil {
		t.Fatal(err)
	}
	if newerID <= olderID {
		t.Fatalf("ID with later encoded timestamp %q <= %q", newerID, olderID)
	}
}

func TestGeneratorConcurrentCallsAreUnique(t *testing.T) {
	const count = 128
	g := newGenerator(func() time.Time { return time.UnixMilli(1_800_000_000_000) }, bytes.NewReader(make([]byte, 10)))
	ids := make([]ID, count)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := g.New()
			if err != nil {
				t.Errorf("New(): %v", err)
				return
			}
			ids[i] = id
		}(i)
	}
	wg.Wait()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatalf("duplicate TaskID %q", ids[i])
		}
	}
}

func TestParseRejectsNonCanonicalIDs(t *testing.T) {
	valid := prefix + strings.Repeat("0", 26)
	maxValid := prefix + "7" + strings.Repeat("Z", 25)
	if _, err := Parse(maxValid); err != nil {
		t.Fatalf("Parse(max valid ULID): %v", err)
	}
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "wrong prefix", value: "task-" + strings.Repeat("0", 26)},
		{name: "wrong length", value: valid[:len(valid)-1]},
		{name: "lowercase", value: prefix + strings.Repeat("a", 26)},
		{name: "excluded alphabet character", value: prefix + strings.Repeat("I", 26)},
		{name: "overflowing first digit", value: prefix + "8" + strings.Repeat("0", 25)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.value); !errors.Is(err, ErrInvalidID) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalidID", tc.value, err)
			}
		})
	}
}

func TestGeneratorRejectsTimestampOutsideULIDRange(t *testing.T) {
	g := newGenerator(func() time.Time { return time.UnixMilli(-1) }, bytes.NewReader(make([]byte, 10)))
	if _, err := g.New(); !errors.Is(err, ErrTimestampRange) {
		t.Fatalf("New() error = %v, want ErrTimestampRange", err)
	}
}

func TestGeneratorReportsEntropyFailure(t *testing.T) {
	g := newGenerator(func() time.Time { return time.UnixMilli(1_800_000_000_000) }, bytes.NewReader(nil))
	if _, err := g.New(); !errors.Is(err, ErrNoEntropy) {
		t.Fatalf("New() error = %v, want ErrNoEntropy", err)
	}
}

func TestGeneratorFailsWhenMonotonicEntropyOverflows(t *testing.T) {
	g := newGenerator(func() time.Time { return time.UnixMilli(1_800_000_000_000) }, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if _, err := g.New(); err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	if _, err := g.New(); !errors.Is(err, ErrEntropyOverflow) {
		t.Fatalf("second New() error = %v, want ErrEntropyOverflow", err)
	}
}

func TestGeneratorCarriesMonotonicEntropyAcrossBytes(t *testing.T) {
	initialEntropy := [10]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x01, 0xff}
	g := newGenerator(func() time.Time { return time.UnixMilli(1_800_000_000_000) }, bytes.NewReader(initialEntropy[:]))
	first, err := g.New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.New()
	if err != nil {
		t.Fatal(err)
	}
	_, firstEntropy, err := decode(strings.TrimPrefix(first.String(), prefix))
	if err != nil {
		t.Fatal(err)
	}
	_, secondEntropy, err := decode(strings.TrimPrefix(second.String(), prefix))
	if err != nil {
		t.Fatal(err)
	}
	wantEntropy := initialEntropy
	wantEntropy[8]++
	wantEntropy[9] = 0
	if firstEntropy != initialEntropy || secondEntropy != wantEntropy {
		t.Fatalf("entropy sequence = (%x, %x), want (%x, %x)", firstEntropy, secondEntropy, initialEntropy, wantEntropy)
	}
}

func TestGeneratorCarriesMonotonicEntropyAcrossAllBytes(t *testing.T) {
	initialEntropy := [10]byte{0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	g := newGenerator(func() time.Time { return time.UnixMilli(1_800_000_000_000) }, bytes.NewReader(initialEntropy[:]))
	if _, err := g.New(); err != nil {
		t.Fatal(err)
	}
	second, err := g.New()
	if err != nil {
		t.Fatal(err)
	}
	_, gotEntropy, err := decode(strings.TrimPrefix(second.String(), prefix))
	if err != nil {
		t.Fatal(err)
	}
	wantEntropy := [10]byte{0x01}
	if gotEntropy != wantEntropy {
		t.Fatalf("second entropy = %x, want %x", gotEntropy, wantEntropy)
	}
}

func TestTimestampRejectsInvalidID(t *testing.T) {
	if _, err := ID("task_invalid").Timestamp(); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Timestamp() error = %v, want ErrInvalidID", err)
	}
}
