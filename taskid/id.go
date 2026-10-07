// Package taskid creates and validates Coord's canonical Task identifiers.
package taskid

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	prefix  = "task_"
	base32  = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	maxTime = uint64(1<<48 - 1)
)

var (
	ErrInvalidID       = errors.New("invalid task id")
	ErrTimestampRange  = errors.New("task id timestamp out of ULID range")
	ErrEntropyOverflow = errors.New("task id entropy exhausted for timestamp")
	ErrNoEntropy       = errors.New("task id entropy unavailable")

	defaultGenerator = newGenerator(time.Now, rand.Reader)
)

// ID is Coord's business-opaque Task identifier. Its ULID component includes
// a millisecond timestamp; callers must not infer workflow or business state.
type ID string

// New generates a TaskID using the process-wide monotonic ULID generator.
func New() (ID, error) {
	return defaultGenerator.New()
}

// Parse validates a canonical TaskID and returns its typed representation.
func Parse(value string) (ID, error) {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+26 {
		return "", ErrInvalidID
	}
	if _, _, err := decode(value[len(prefix):]); err != nil {
		return "", err
	}
	return ID(value), nil
}

// Timestamp returns the millisecond timestamp encoded in id. It is an
// approximate technical creation time, not an authoritative Task.CreatedAt.
func (id ID) Timestamp() (time.Time, error) {
	if !strings.HasPrefix(string(id), prefix) || len(id) != len(prefix)+26 {
		return time.Time{}, ErrInvalidID
	}
	ms, _, err := decode(string(id)[len(prefix):])
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(int64(ms)).UTC(), nil
}

func decode(encoded string) (uint64, [10]byte, error) {
	var randomness [10]byte
	if len(encoded) != 26 {
		return 0, randomness, ErrInvalidID
	}
	var raw [16]byte
	for i, char := range []byte(encoded) {
		v := strings.IndexByte(base32, char)
		if v < 0 {
			return 0, randomness, ErrInvalidID
		}
		for j := 0; j < 5; j++ {
			bitPos := i*5 + j - 2
			bit := byte(v >> (4 - j) & 1)
			if bitPos < 0 {
				if bit != 0 {
					return 0, randomness, ErrInvalidID
				}
				continue
			}
			if bitPos >= 128 {
				return 0, randomness, ErrInvalidID
			}
			raw[bitPos/8] |= bit << (7 - bitPos%8)
		}
	}
	var timestamp uint64
	for _, b := range raw[:6] {
		timestamp = timestamp<<8 | uint64(b)
	}
	copy(randomness[:], raw[6:])
	return timestamp, randomness, nil
}

// Validate reports whether id is a canonical TaskID.
func (id ID) Validate() error {
	_, err := Parse(string(id))
	return err
}

// String returns the canonical textual representation.
func (id ID) String() string { return string(id) }

// Generator produces ULIDs that are monotonic within this process, including
// when the clock repeats or moves backwards. Separate processes use independent
// cryptographic entropy; they do not have a globally ordered sequence.
type Generator struct {
	mu          sync.Mutex
	now         func() time.Time
	entropy     io.Reader
	lastMS      uint64
	lastRandom  [10]byte
	initialized bool
}

func newGenerator(now func() time.Time, entropy io.Reader) *Generator {
	if now == nil {
		now = time.Now
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	return &Generator{now: now, entropy: entropy}
}

// New generates a prefixed, canonical ULID. The timestamp portion is sourced
// from UTC Unix milliseconds; if time does not advance, its entropy suffix is
// incremented to preserve local monotonicity.
func (g *Generator) New() (ID, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := g.now().UTC().UnixMilli()
	if ms < 0 || uint64(ms) > maxTime {
		return "", ErrTimestampRange
	}
	timestamp := uint64(ms)
	var randomness [10]byte
	if g.initialized && timestamp <= g.lastMS {
		timestamp = g.lastMS
		randomness = g.lastRandom
		if !increment(&randomness) {
			return "", ErrEntropyOverflow
		}
	} else if _, err := io.ReadFull(g.entropy, randomness[:]); err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoEntropy, err)
	}

	g.lastMS = timestamp
	g.lastRandom = randomness
	g.initialized = true
	return ID(prefix + encode(timestamp, randomness)), nil
}

func increment(value *[10]byte) bool {
	for i := len(value) - 1; i >= 0; i-- {
		value[i]++
		if value[i] != 0 {
			return true
		}
	}
	return false
}

func encode(timestamp uint64, randomness [10]byte) string {
	var raw [16]byte
	for i := 5; i >= 0; i-- {
		raw[i] = byte(timestamp)
		timestamp >>= 8
	}
	copy(raw[6:], randomness[:])

	var out [26]byte
	for i := range out {
		var value byte
		for j := 0; j < 5; j++ {
			value <<= 1
			bitPos := i*5 + j - 2 // ULID uses two leading zero padding bits.
			if bitPos >= 0 {
				value |= raw[bitPos/8] >> (7 - bitPos%8) & 1
			}
		}
		out[i] = base32[value]
	}
	return string(out[:])
}
