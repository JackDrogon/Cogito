package provider

import (
	"bytes"
	"strings"
	"testing"
)

func TestCappedWriterUnderCapPassthrough(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writer := NewCappedWriter(&buf, 10)

	if _, err := writer.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := buf.String(); got != "hello" {
		t.Fatalf("buffer = %q, want passthrough", got)
	}
}

func TestCappedWriterExactBoundary(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writer := NewCappedWriter(&buf, 5)

	if _, err := writer.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if got := buf.String(); got != "hello" {
		t.Fatalf("buffer = %q, want exact payload without marker", got)
	}
}

func TestCappedWriterOverCapSuppressesWithSingleMarker(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writer := NewCappedWriter(&buf, 5)

	if _, err := writer.Write([]byte("hello world")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := writer.Write([]byte(" ignored")); err != nil {
		t.Fatalf("second Write() error = %v", err)
	}

	marker := truncationMarker(5)
	if got := buf.String(); got != "hello"+marker {
		t.Fatalf("buffer = %q, want capped payload plus marker", got)
	}
}

func TestCappedWriterMarkerWrittenOnceAcrossManyWrites(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writer := NewCappedWriter(&buf, 3)

	for _, data := range []string{"ab", "cd", "ef", "gh"} {
		if _, err := writer.Write([]byte(data)); err != nil {
			t.Fatalf("Write(%q) error = %v", data, err)
		}
	}

	marker := truncationMarker(3)
	if got := strings.Count(buf.String(), marker); got != 1 {
		t.Fatalf("marker count = %d, want 1 in %q", got, buf.String())
	}
	if got := buf.String(); got != "abc"+marker {
		t.Fatalf("buffer = %q, want capped payload plus one marker", got)
	}
}
