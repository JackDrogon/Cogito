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
	case "StepQueued":
		fmt.Fprintf(v.writer, "[verbose] Step queued: %s\n", event.StepID)
	case "StepStarted":
		msg := strings.TrimSpace(event.Message)
		sessionID := event.Data["provider_session_id"]
		if msg != "" {
			fmt.Fprintf(v.writer, "[verbose] Step started: %s - %s (session: %s)\n", event.StepID, msg, sessionID)
		} else {
			fmt.Fprintf(v.writer, "[verbose] Step started: %s (session: %s)\n", event.StepID, sessionID)
		}
	case "StepSucceeded":
		summary := extractSummary(event.Data)
		fmt.Fprintf(v.writer, "[verbose] Step succeeded: %s - %s\n", event.StepID, summary)
	case "StepFailed":
		summary := extractSummary(event.Data)
		fmt.Fprintf(v.writer, "[verbose] Step failed: %s - %s\n", event.StepID, summary)
	case "RunWaitingApproval":
		fmt.Fprintf(v.writer, "[verbose] Waiting for approval: %s\n", event.Message)
	case "ApprovalGranted":
		fmt.Fprintf(v.writer, "[verbose] Approval granted: %s\n", event.StepID)
	case "RunStarted":
		fmt.Fprintf(v.writer, "[verbose] Run started\n")
	case "RunSucceeded":
		fmt.Fprintf(v.writer, "[verbose] Run succeeded\n")
	case "RunFailed":
		fmt.Fprintf(v.writer, "[verbose] Run failed: %s\n", event.Message)
	}
}

func extractSummary(data map[string]string) string {
	if data == nil {
		return ""
	}
	return data["summary"]
}
