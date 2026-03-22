package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var secretSummaryPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key\s*[:=]\s*)([^\s,;]+)`),
	regexp.MustCompile(`(?i)(token\s*[:=]\s*)([^\s,;]+)`),
	regexp.MustCompile(`(?i)(password\s*[:=]\s*)([^\s,;]+)`),
	regexp.MustCompile(`(?i)(secret\s*[:=]\s*)([^\s,;]+)`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)([^\s,;]+)`),
}

func sanitizeArtifacts(runDir string, artifacts []ArtifactRecord) ([]ArtifactRecord, error) {
	cleaned := make([]ArtifactRecord, 0, len(artifacts))

	for _, artifact := range artifacts {
		normalized, err := sanitizeArtifact(runDir, artifact)
		if err != nil {
			return nil, err
		}

		cleaned = append(cleaned, normalized)
	}

	return cleaned, nil
}

func sanitizeArtifact(runDir string, artifact ArtifactRecord) (ArtifactRecord, error) {
	path, fullPath, err := sanitizeArtifactPath(runDir, artifact.Path)
	if err != nil {
		return ArtifactRecord{}, wrapError(ErrorCodeArtifacts, "validate artifact path", err)
	}

	digest, err := digestFile(fullPath)
	if err != nil {
		return ArtifactRecord{}, wrapError(ErrorCodeArtifacts, "hash artifact", err)
	}

	artifact.Path = path
	artifact.Digest = digest
	artifact.Kind = strings.TrimSpace(artifact.Kind)
	artifact.StepID = strings.TrimSpace(artifact.StepID)
	artifact.Summary = redactSummary(artifact.Summary)
	artifact.CreatedAt = strings.TrimSpace(artifact.CreatedAt)

	return artifact, nil
}

func sanitizeCheckpoint(checkpoint *Checkpoint) *Checkpoint {
	if checkpoint == nil {
		return nil
	}

	clone := *checkpoint
	if checkpoint.Steps == nil {
		return &clone
	}

	clone.Steps = make(map[string]StepCheckpoint, len(checkpoint.Steps))

	for stepID, step := range checkpoint.Steps {
		step.Summary = redactSummary(step.Summary)
		clone.Steps[stepID] = step
	}

	return &clone
}

func sanitizeArtifactPath(runDir, artifactPath string) (string, string, error) {
	runDir = filepath.Clean(runDir)

	artifactPath = strings.TrimSpace(artifactPath)
	if artifactPath == "" {
		return "", "", artifactPathError("artifact path is required")
	}

	if filepath.IsAbs(artifactPath) {
		return "", "", artifactPathError("artifact path must be relative")
	}

	clean := filepath.Clean(artifactPath)
	if clean == "." {
		return "", "", artifactPathError("artifact path is required")
	}

	fullPath := filepath.Join(runDir, clean)

	rel, err := filepath.Rel(runDir, fullPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve artifact path: %w", err)
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", artifactPathError("path escapes run directory")
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", artifactPathError("artifact does not exist")
		}

		return "", "", err
	}

	if info.IsDir() {
		return "", "", artifactPathError("artifact path must reference a file")
	}

	return filepath.ToSlash(rel), fullPath, nil
}

func digestFile(path string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

func redactSummary(summary string) string {
	redacted := strings.TrimSpace(summary)
	for _, pattern := range secretSummaryPatterns {
		redacted = pattern.ReplaceAllString(redacted, `${1}[REDACTED]`)
	}

	return redacted
}
