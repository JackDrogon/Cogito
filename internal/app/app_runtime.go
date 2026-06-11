// Package-level command collaborators. The command tables in app.go are
// package globals, so their service dependencies live here as well; tests may
// swap diagnosticOutput to capture diagnostics.
package app

import (
	"io"
	"os"
)

var (
	runs       runService
	presenter  textPresenter
	appService = newApplicationService()

	// DiagOut receives warnings and diagnostics that must not pollute the
	// machine-readable stdout stream (POSIX convention: diagnostics on
	// stderr).
	diagnosticOutput io.Writer = os.Stderr
)
