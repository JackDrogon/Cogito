package provider

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactingWriterSingleWrite(t *testing.T) {
	t.Parallel()

	got, err := writeRedacted(t, []string{"supersecretvalue"}, []string{"got supersecretvalue\n"})
	if err != nil {
		t.Fatalf("redacting write: %v", err)
	}

	if got != "got "+redactionMarker+"\n" {
		t.Fatalf("redacted output = %q", got)
	}
}

func TestRedactingWriterSplitSecret(t *testing.T) {
	t.Parallel()

	got, err := writeRedacted(t, []string{"supersecretvalue"}, []string{"got super", "secretvalue\n"})
	if err != nil {
		t.Fatalf("redacting write: %v", err)
	}

	if got != "got "+redactionMarker+"\n" {
		t.Fatalf("redacted output = %q", got)
	}
}

func TestRedactingWriterMultipleSecrets(t *testing.T) {
	t.Parallel()

	got, err := writeRedacted(t, []string{"firstsecret", "secondsecret"}, []string{"firstsecret and secondsecret"})
	if err != nil {
		t.Fatalf("redacting write: %v", err)
	}

	want := redactionMarker + " and " + redactionMarker
	if got != want {
		t.Fatalf("redacted output = %q, want %q", got, want)
	}
}

func TestRedactingWriterSecretAtEndFlushesOnClose(t *testing.T) {
	t.Parallel()

	got, err := writeRedacted(t, []string{"trailingsecret"}, []string{"tail trailingsecret"})
	if err != nil {
		t.Fatalf("redacting write: %v", err)
	}

	if got != "tail "+redactionMarker {
		t.Fatalf("redacted output = %q", got)
	}
}

func TestCollectSecretEnvValuesSortsLongestFirst(t *testing.T) {
	t.Parallel()

	secrets := collectSecretEnvValues([]string{
		"COGITO_TEST_TOKEN=secretvalue123456",
		"COGITO_TEST_SECRET=secretvalue123",
		"COGITO_TEST_PASSWORD=short",
		"COGITO_TEST_PUBLIC=notredactedvalue",
	})
	got, err := writeRedacted(t, secrets, []string{"secretvalue123456 secretvalue123"})
	if err != nil {
		t.Fatalf("redacting write: %v", err)
	}

	want := redactionMarker + " " + redactionMarker
	if got != want {
		t.Fatalf("redacted output = %q, want %q", got, want)
	}
}

func TestStartProcessRedactsLogAndExtraSinkOnly(t *testing.T) {
	const secret = "supersecretvalue123"

	t.Setenv("COGITO_TEST_FAKE_TOKEN", secret)

	logPath := filepath.Join(t.TempDir(), "provider.log")
	sink := &syncBuffer{}
	session, err := StartProcess(context.Background(), ProcessRequest{
		Binary:    "/bin/bash",
		Args:      []string{"-c", "echo got $COGITO_TEST_FAKE_TOKEN"},
		LogPath:   logPath,
		ExtraSink: sink,
	})
	if err != nil {
		t.Fatalf("StartProcess returned error: %v", err)
	}

	result := awaitResult(t, session, 2*time.Second)
	if result.Err != nil {
		t.Fatalf("ProcessResult.Err = %v, want nil", result.Err)
	}

	stdout := string(result.Stdout)
	if !strings.Contains(stdout, secret) {
		t.Fatalf("ProcessResult.Stdout = %q, want raw secret", stdout)
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	assertRedactedOutput(t, string(contents), secret)
	assertRedactedOutput(t, sink.String(), secret)
}

func writeRedacted(t *testing.T, secrets, chunks []string) (string, error) {
	t.Helper()

	var buf bytes.Buffer
	writer := newRedactingWriter(&buf, secrets)
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			return "", err
		}
	}

	if err := writer.Close(); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func assertRedactedOutput(t *testing.T, output, secret string) {
	t.Helper()

	if strings.Contains(output, secret) {
		t.Fatalf("output = %q, must not contain secret", output)
	}
	if !strings.Contains(output, redactionMarker) {
		t.Fatalf("output = %q, want redaction marker", output)
	}
}
