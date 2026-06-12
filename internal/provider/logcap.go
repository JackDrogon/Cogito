package provider

import (
	"fmt"
	"io"
)

// DefaultMaxLogBytes caps durable provider and command output artifacts so a
// runaway agent or command cannot fill the disk. The cap applies only to files
// written for audit/readback; live ExtraSink output and in-memory stdout/stderr
// buffers remain uncapped so session scanning and structured result parsing see
// the full child output.
const DefaultMaxLogBytes int64 = 64 << 20

type cappedWriter struct {
	target io.Writer
	limit  int64
	wrote  int64
	done   bool
}

// NewCappedWriter returns a writer that forwards at most limit bytes to target,
// then appends one truncation marker and silently accepts all later writes. If a
// write crosses the boundary, bytes up to the exact cap are forwarded before the
// marker; values <= 0 use DefaultMaxLogBytes.
func NewCappedWriter(target io.Writer, limit int64) io.Writer {
	if limit <= 0 {
		limit = DefaultMaxLogBytes
	}

	return &cappedWriter{target: target, limit: limit}
}

func (w *cappedWriter) Write(data []byte) (int, error) {
	if w.done {
		return len(data), nil
	}

	remaining := w.limit - w.wrote
	if remaining > 0 {
		toWrite := data
		if int64(len(toWrite)) > remaining {
			toWrite = data[:remaining]
		}

		bytesWritten, err := w.target.Write(toWrite)
		w.wrote += int64(bytesWritten)

		if err != nil {
			return len(data), err
		}
	}

	if int64(len(data)) > remaining {
		w.done = true
		_, err := io.WriteString(w.target, truncationMarker(w.limit))

		return len(data), err
	}

	return len(data), nil
}

func truncationMarker(limit int64) string {
	return fmt.Sprintf("\n[runner] log truncated: %d-byte cap reached; further output omitted\n", limit)
}
