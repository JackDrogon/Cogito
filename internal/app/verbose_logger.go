package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/JackDrogon/Cogito/internal/store"
)

type verboseLogger struct {
	enabled bool
	writer  io.Writer
}

func newVerboseLogger(enabled bool, writer io.Writer) *verboseLogger {
	return &verboseLogger{enabled: enabled, writer: writer}
}

func (v *verboseLogger) logEvent(event store.Event) {
	if !v.enabled || v.writer == nil {
		return
	}

	switch event.Type {
	case store.EventStepQueued:
		fmt.Fprintf(v.writer, "[verbose] Step queued: %s\n", event.StepID)
	case store.EventStepStarted:
		msg := strings.TrimSpace(event.Message)
		sessionID := event.Data["provider_session_id"]

		if msg != "" {
			fmt.Fprintf(v.writer, "[verbose] Step started: %s - %s (session: %s)\n", event.StepID, msg, sessionID)
		} else {
			fmt.Fprintf(v.writer, "[verbose] Step started: %s (session: %s)\n", event.StepID, sessionID)
		}
	case store.EventStepSucceeded:
		summary := extractSummary(event.Data)
		fmt.Fprintf(v.writer, "[verbose] Step succeeded: %s - %s\n", event.StepID, summary)
	case store.EventStepFailed:
		summary := extractSummary(event.Data)
		fmt.Fprintf(v.writer, "[verbose] Step failed: %s - %s\n", event.StepID, summary)
	case store.EventRunWaitingApproval:
		fmt.Fprintf(v.writer, "[verbose] Waiting for approval: %s\n", event.Message)
	case store.EventApprovalGranted:
		fmt.Fprintf(v.writer, "[verbose] Approval granted: %s\n", event.StepID)
	case store.EventRunStarted:
		fmt.Fprintf(v.writer, "[verbose] Run started\n")
	case store.EventRunSucceeded:
		fmt.Fprintf(v.writer, "[verbose] Run succeeded\n")
	case store.EventRunFailed:
		fmt.Fprintf(v.writer, "[verbose] Run failed: %s\n", event.Message)
	case store.EventRunCreated,
		store.EventRunPaused,
		store.EventRunCanceled,
		store.EventStepRetried,
		store.EventApprovalRequested,
		store.EventApprovalDenied,
		store.EventApprovalTimedOut,
		store.EventReplayStarted,
		store.EventReplaySucceeded,
		store.EventReplayFailed:
		return
	}
}

func extractSummary(data map[string]string) string {
	if data == nil {
		return ""
	}

	return data["summary"]
}
