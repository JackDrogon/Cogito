package app

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/JackDrogon/Cogito/internal/store"
)

// verboseTimeLayout is the console timestamp format shared by verbose event
// lines; it matches the live provider output prefix for visual alignment.
const verboseTimeLayout = "2006-01-02 15:04:05"

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
		v.printf(event, "Step queued: %s", event.StepID)
	case store.EventStepStarted:
		msg := strings.TrimSpace(event.Message)
		sessionID := event.Data["provider_session_id"]

		if msg != "" {
			v.printf(event, "Step started: %s - %s (session: %s)", event.StepID, msg, sessionID)
		} else {
			v.printf(event, "Step started: %s (session: %s)", event.StepID, sessionID)
		}
	case store.EventStepResumed:
		msg := strings.TrimSpace(event.Message)
		sessionID := event.Data["provider_session_id"]

		if msg != "" {
			v.printf(event, "Step resumed: %s - %s (session: %s)", event.StepID, msg, sessionID)
		} else {
			v.printf(event, "Step resumed: %s (session: %s)", event.StepID, sessionID)
		}
	case store.EventStepSucceeded:
		v.printf(event, "Step succeeded: %s - %s", event.StepID, extractSummary(event.Data))
	case store.EventStepFailed:
		v.printf(event, "Step failed: %s - %s", event.StepID, extractSummary(event.Data))
	case store.EventRunWaitingApproval:
		v.printf(event, "Waiting for approval: %s", event.Message)
	case store.EventApprovalGranted:
		v.printf(event, "Approval granted: %s", event.StepID)
	case store.EventRunStarted:
		v.printf(event, "Run started")
	case store.EventRunSucceeded:
		v.printf(event, "Run succeeded")
	case store.EventRunFailed:
		v.printf(event, "Run failed: %s", event.Message)
	case store.EventRunCreated,
		store.EventRunPaused,
		store.EventRunCanceled,
		store.EventStepRetried,
		store.EventStepInterrupted,
		store.EventApprovalRequested,
		store.EventApprovalDenied,
		store.EventApprovalTimedOut,
		store.EventReplayStarted,
		store.EventReplaySucceeded,
		store.EventReplayFailed:
		return
	}
}

// printf renders one verbose line prefixed with the event's durable timestamp
// (data.occurred_at), so the replay reflects when each transition actually
// happened rather than when it was printed.
func (v *verboseLogger) printf(event store.Event, format string, args ...any) {
	fmt.Fprintf(v.writer, "%s [verbose] %s\n", eventTimestamp(event), fmt.Sprintf(format, args...))
}

// eventTimestamp extracts and formats the durable occurred_at timestamp; an
// absent or unparseable value degrades to a placeholder of equal width so the
// columns stay aligned.
func eventTimestamp(event store.Event) string {
	occurredAt, err := time.Parse(time.RFC3339Nano, event.Data["occurred_at"])
	if err != nil {
		return "---------- --:--:--"
	}

	return occurredAt.Local().Format(verboseTimeLayout)
}

func extractSummary(data map[string]string) string {
	if data == nil {
		return ""
	}

	return data["summary"]
}
