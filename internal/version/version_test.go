package version

import "testing"

func TestInfo(t *testing.T) {
	oldVersion := Version
	Version = "test"
	defer func() {
		Version = oldVersion
	}()

	if got := Info(); got != "test" {
		t.Fatalf("Info() = %q, want %q", got, "test")
	}
}
