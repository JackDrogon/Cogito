package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JackDrogon/Cogito/internal/version"
)

func TestRun(t *testing.T) {
	t.Run("default greeting", func(t *testing.T) {
		var out bytes.Buffer

		if err := Run(nil, &out); err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if out.String() != "Hello, Cogito!\n" {
			t.Fatalf("Run() output = %q", out.String())
		}
	})

	t.Run("custom greeting", func(t *testing.T) {
		var out bytes.Buffer

		if err := Run([]string{"--name", "team"}, &out); err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if out.String() != "Hello, team!\n" {
			t.Fatalf("Run() output = %q", out.String())
		}
	})

	t.Run("version output", func(t *testing.T) {
		oldVersion := version.Version
		version.Version = "v1.2.3"
		defer func() {
			version.Version = oldVersion
		}()

		var out bytes.Buffer
		if err := Run([]string{"--version"}, &out); err != nil {
			t.Fatalf("Run() error = %v", err)
		}

		if out.String() != "v1.2.3\n" {
			t.Fatalf("Run() output = %q", out.String())
		}
	})

	t.Run("unexpected args", func(t *testing.T) {
		var out bytes.Buffer
		err := Run([]string{"extra"}, &out)
		if err == nil {
			t.Fatal("Run() expected error, got nil")
		}
		if !strings.Contains(err.Error(), "unexpected arguments") {
			t.Fatalf("Run() error = %v", err)
		}
	})
}
