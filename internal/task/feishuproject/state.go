package feishuproject

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/JackDrogon/Cogito/internal/store"
)

// State is the on-disk record of the previously observed story set. The
// fingerprint per story lets Watch detect content changes between pulls.
type State struct {
	UpdatedAt    time.Time         `json:"updated_at"`
	Fingerprints map[string]string `json:"fingerprints"`
}

// NewState returns an empty state usable when no file exists yet.
func NewState() State {
	return State{Fingerprints: map[string]string{}}
}

// LoadState reads a previously written state file. A missing file is a
// non-error and returns an empty State so first-run callers don't branch.
func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NewState(), nil
		}

		return State{}, fmt.Errorf("feishuproject: read state %s: %w", path, err)
	}

	state := NewState()
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("feishuproject: decode state %s: %w", path, err)
	}

	if state.Fingerprints == nil {
		state.Fingerprints = map[string]string{}
	}

	return state, nil
}

// SaveState writes the state atomically by renaming a sibling temp file.
// Atomic replace keeps the file consistent if the process is killed mid-write.
func SaveState(path string, state State) error {
	if err := store.EnsureSelfIgnored(path); err != nil {
		return fmt.Errorf("feishuproject: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("feishuproject: mkdir state dir: %w", err)
	}

	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("feishuproject: encode state: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return fmt.Errorf("feishuproject: write state tmp: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("feishuproject: rename state file: %w", err)
	}

	return nil
}

// Diff is the structured comparison of a fresh story set against the
// previously persisted State. IDs sort numerically for stable output.
type Diff struct {
	Added   []int64 `json:"added"`
	Updated []int64 `json:"updated"`
	Removed []int64 `json:"removed"`
}

// IsEmpty reports whether there were no changes between the two snapshots.
func (d Diff) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Updated) == 0 && len(d.Removed) == 0
}

// BuildDiff compares the current pull against the prior state and produces
// the additions, updates, and removals. The returned next State has the
// pull's UpdatedAt and the fresh fingerprint map.
func BuildDiff(prev State, stories []Story, now time.Time) (Diff, State) {
	diff := Diff{}
	next := State{
		UpdatedAt:    now,
		Fingerprints: make(map[string]string, len(stories)),
	}

	seen := make(map[string]bool, len(stories))

	for i := range stories {
		story := &stories[i]
		key := storyKey(story.ID)
		fingerprint := story.Fingerprint()
		next.Fingerprints[key] = fingerprint
		seen[key] = true

		prior, existed := prev.Fingerprints[key]

		switch {
		case !existed:
			diff.Added = append(diff.Added, story.ID)
		case prior != fingerprint:
			diff.Updated = append(diff.Updated, story.ID)
		}
	}

	for key := range prev.Fingerprints {
		if seen[key] {
			continue
		}

		id, err := parseStoryKey(key)
		if err != nil {
			continue
		}

		diff.Removed = append(diff.Removed, id)
	}

	sort.Slice(diff.Added, func(i, j int) bool { return diff.Added[i] < diff.Added[j] })
	sort.Slice(diff.Updated, func(i, j int) bool { return diff.Updated[i] < diff.Updated[j] })
	sort.Slice(diff.Removed, func(i, j int) bool { return diff.Removed[i] < diff.Removed[j] })

	return diff, next
}
