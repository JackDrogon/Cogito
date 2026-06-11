package feishuproject

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/store"
)

// Snapshot is the full document persisted to OutputFile and printed to stdout.
// Storing the diff alongside the story list lets downstream tooling react to
// changes without re-deriving them.
type Snapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	ProjectKey  string    `json:"project_key"`
	Count       int       `json:"count"`
	Diff        Diff      `json:"diff"`
	Stories     []Story   `json:"stories"`
}

// WriteSnapshot serializes the snapshot to disk as pretty-printed JSON via
// an atomic rename. Path may include directories that do not yet exist.
func WriteSnapshot(path string, snapshot Snapshot) error {
	if err := store.EnsureSelfIgnored(path); err != nil {
		return fmt.Errorf("feishuproject: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("feishuproject: mkdir output dir: %w", err)
	}

	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("feishuproject: encode snapshot: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o644); err != nil {
		return fmt.Errorf("feishuproject: write snapshot tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("feishuproject: rename snapshot: %w", err)
	}

	return nil
}

// storyKey returns the canonical map key used in State.Fingerprints.
// Encoding IDs as strings keeps the on-disk JSON format stable when serialized.
func storyKey(id int64) string {
	return strconv.FormatInt(id, 10)
}

func parseStoryKey(key string) (int64, error) {
	return strconv.ParseInt(key, 10, 64)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// JoinOwners renders an owner slice for plain-text output (CLI summaries).
func JoinOwners(owners []string) string {
	return strings.Join(owners, ", ")
}
