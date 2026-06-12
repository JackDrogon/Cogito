package provider

import (
	"bytes"
	"io"
	"sort"
	"strings"
)

const redactionMarker = "***REDACTED***"

var secretEnvNameMarkers = []string{
	"TOKEN",
	"SECRET",
	"PASSWORD",
	"PASSWD",
	"CREDENTIAL",
	"API_KEY",
	"APIKEY",
	"PRIVATE_KEY",
	"AUTH",
}

func collectSecretEnvValues(environ []string) []string {
	values := make(map[string]struct{})

	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || len(value) < 8 || !isSecretEnvName(name) {
			continue
		}

		values[value] = struct{}{}
	}

	secrets := make([]string, 0, len(values))
	for value := range values {
		secrets = append(secrets, value)
	}

	sort.Slice(secrets, func(i, j int) bool {
		if len(secrets[i]) == len(secrets[j]) {
			return secrets[i] < secrets[j]
		}

		return len(secrets[i]) > len(secrets[j])
	})

	return secrets
}

func isSecretEnvName(name string) bool {
	upperName := strings.ToUpper(name)

	for _, marker := range secretEnvNameMarkers {
		if strings.Contains(upperName, marker) {
			return true
		}
	}

	return false
}

type redactingWriter struct {
	target  io.Writer
	secrets [][]byte
	pending []byte
	err     error
	closed  bool
}

func newRedactingWriter(target io.Writer, secrets []string) io.WriteCloser {
	writer := &redactingWriter{target: target}

	for _, secret := range secrets {
		if secret == "" {
			continue
		}

		writer.secrets = append(writer.secrets, []byte(secret))
	}

	return writer
}

func (w *redactingWriter) Write(data []byte) (int, error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}

	if len(w.secrets) == 0 {
		_, err := w.target.Write(data)

		return len(data), err
	}

	w.pending = append(w.pending, data...)
	w.drainPending(false)

	return len(data), w.err
}

func (w *redactingWriter) Close() error {
	if w.closed {
		return w.err
	}

	w.drainPending(true)
	w.closed = true

	return w.err
}

func (w *redactingWriter) drainPending(final bool) {
	for len(w.pending) > 0 && w.err == nil {
		if secret, ok := w.matchSecret(); ok {
			w.writeBytes([]byte(redactionMarker))
			w.pending = w.pending[len(secret):]

			continue
		}

		if !final && w.hasSecretPrefix() {
			return
		}

		w.writeBytes(w.pending[:1])
		w.pending = w.pending[1:]
	}
}

func (w *redactingWriter) matchSecret() ([]byte, bool) {
	for _, secret := range w.secrets {
		if bytes.HasPrefix(w.pending, secret) {
			return secret, true
		}
	}

	return nil, false
}

func (w *redactingWriter) hasSecretPrefix() bool {
	for _, secret := range w.secrets {
		if bytes.HasPrefix(secret, w.pending) {
			return true
		}
	}

	return false
}

func (w *redactingWriter) writeBytes(data []byte) {
	if len(data) == 0 || w.err != nil {
		return
	}

	_, w.err = w.target.Write(data)
}
