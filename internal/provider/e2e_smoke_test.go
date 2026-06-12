//go:build e2e

package provider_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JackDrogon/Cogito/internal/provider"
	_ "github.com/JackDrogon/Cogito/internal/provider/claude"
	_ "github.com/JackDrogon/Cogito/internal/provider/codex"
	_ "github.com/JackDrogon/Cogito/internal/provider/opencode"
)

const e2ePrompt = "Reply with exactly: OK. Do not run any commands. Do not modify any files."

type e2eProviderCase struct {
	Name   string
	Binary string
}

func TestE2EProviderSmoke(t *testing.T) {
	cases := []e2eProviderCase{
		{Name: "codex", Binary: "codex"},
		{Name: "claude", Binary: "claude"},
		{Name: "opencode", Binary: "opencode"},
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			runE2ESmoke(t, tc)
		})
	}
}

func runE2ESmoke(t *testing.T, tc e2eProviderCase) {
	t.Helper()
	skipUnlessE2EEnabled(t, tc)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)

	logDir := t.TempDir()
	adapter := buildE2EAdapter(t, tc.Name, logDir)
	execution := startE2EExecution(t, ctx, adapter)
	terminal := collectE2EExecution(t, e2ePollParams{Ctx: ctx, Adapter: adapter, Execution: execution})
	result := normalizeE2EResult(t, e2eNormalizeParams{Ctx: ctx, Adapter: adapter, Execution: terminal})

	if result.Status != provider.ExecutionStateSucceeded {
		t.Fatalf("StepResult.Status = %q, want %q; summary: %s", result.Status, provider.ExecutionStateSucceeded, result.Summary)
	}
	if strings.TrimSpace(result.Summary) == "" {
		t.Fatal("StepResult.Summary is empty, want provider response summary")
	}

	t.Logf("provider %s summary: %s", tc.Name, result.Summary)
	t.Logf("provider %s log path: %s", tc.Name, e2eLogPath(logDir, tc))
}

func skipUnlessE2EEnabled(t *testing.T, tc e2eProviderCase) {
	t.Helper()
	if os.Getenv("COGITO_E2E") != "1" {
		t.Skip("COGITO_E2E != 1; skipping real provider CLI smoke test")
	}
	if _, err := exec.LookPath(tc.Binary); err != nil {
		t.Skipf("%s binary %q not found on PATH; skipping real provider CLI smoke test", tc.Name, tc.Binary)
	}
}

func buildE2EAdapter(t *testing.T, name string, logDir string) provider.Provider {
	t.Helper()
	registration, ok := provider.Lookup(name)
	if !ok {
		t.Fatalf("provider.Lookup(%q) found = false, want registered provider", name)
	}

	return registration.Build(provider.Options{LogDir: logDir})
}

func startE2EExecution(t *testing.T, ctx context.Context, adapter provider.Provider) *provider.Execution {
	t.Helper()
	execution, err := adapter.Start(ctx, provider.StartRequest{
		RunID:      "e2e-run",
		StepID:     "smoke",
		AttemptID:  "attempt-1",
		WorkingDir: t.TempDir(),
		Prompt:     e2ePrompt,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return execution
}

type e2ePollParams struct {
	Ctx       context.Context
	Adapter   provider.Provider
	Execution *provider.Execution
}

func collectE2EExecution(t *testing.T, params e2ePollParams) *provider.Execution {
	t.Helper()
	execution := params.Execution
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for !execution.State.Normalizable() {
		select {
		case <-params.Ctx.Done():
			t.Fatalf("provider execution did not finish before deadline: %v", params.Ctx.Err())
		case <-ticker.C:
		}

		next, err := params.Adapter.PollOrCollect(params.Ctx, execution.Handle)
		if err != nil {
			t.Fatalf("PollOrCollect() error = %v", err)
		}
		execution = next
	}

	return execution
}

type e2eNormalizeParams struct {
	Ctx       context.Context
	Adapter   provider.Provider
	Execution *provider.Execution
}

func normalizeE2EResult(t *testing.T, params e2eNormalizeParams) *provider.StepResult {
	t.Helper()
	result, err := params.Adapter.NormalizeResult(params.Ctx, provider.NormalizeRequest{Execution: params.Execution})
	if err != nil {
		t.Fatalf("NormalizeResult() error = %v", err)
	}

	return result
}

func e2eLogPath(logDir string, tc e2eProviderCase) string {
	return filepath.Join(logDir, "attempt-1-"+tc.Name+".log")
}
