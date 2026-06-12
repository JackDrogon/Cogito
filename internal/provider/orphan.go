package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const orphanProbeInterval = 50 * time.Millisecond

// linuxClockTicksPerSecond is USER_HZ, which is 100 on all supported Linux
// platforms. Using sysconf(SC_CLK_TCK) would require cgo and is not worth it.
const linuxClockTicksPerSecond = 100

// PIDRecord is the on-disk, machine-local process identity written beside a
// provider log. It is intentionally excluded from event logs and checkpoints.
type PIDRecord struct {
	PID       int    `json:"pid"`
	PGID      int    `json:"pgid"`
	Binary    string `json:"binary"`
	StartedAt string `json:"started_at"`
	Label     string `json:"label,omitempty"`
}

// OrphanProcess describes one provider pidfile discovered under a run dir.
// Alive and IdentityMatched must both be true before any signal may be sent.
type OrphanProcess struct {
	Path            string
	StepID          string
	Record          PIDRecord
	Alive           bool
	IdentityMatched bool
	StaleReason     string
}

// ReapReport summarizes the pidfiles inspected by ReapOrphans.
type ReapReport struct {
	Reaped  []OrphanProcess
	Cleaned []OrphanProcess
}

// FindOrphans returns provider pidfiles under rootDir, classified without
// mutating process or file state. Only Alive+IdentityMatched entries are safe to
// present as still-running provider orphans.
func FindOrphans(rootDir string) ([]OrphanProcess, error) {
	pattern := filepath.Join(strings.TrimSpace(rootDir), "provider-logs", "*", "*.pid.json")

	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("provider.FindOrphans: glob pidfiles: %w", err)
	}

	orphans := make([]OrphanProcess, 0, len(paths))
	for _, path := range paths {
		orphan, err := readOrphan(path)
		if err != nil {
			return nil, err
		}

		orphans = append(orphans, orphan)
	}

	return orphans, nil
}

// ReapOrphans terminates confirmed provider orphans under rootDir and removes
// stale pidfiles. A PID is signaled only when it is alive and its /proc cmdline
// matches the recorded binary basename.
func ReapOrphans(rootDir string) (ReapReport, error) {
	orphans, err := FindOrphans(rootDir)
	if err != nil {
		return ReapReport{}, err
	}

	report := ReapReport{}

	for _, orphan := range orphans {
		if orphan.Alive && orphan.IdentityMatched {
			if err := reapConfirmedOrphan(orphan); err != nil {
				return ReapReport{}, err
			}

			removePIDFile(orphan.Path)
			report.Reaped = append(report.Reaped, orphan)

			continue
		}

		removePIDFile(orphan.Path)
		report.Cleaned = append(report.Cleaned, orphan)
	}

	return report, nil
}

func readOrphan(path string) (OrphanProcess, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return OrphanProcess{}, fmt.Errorf("provider.FindOrphans: read pidfile: %w", err)
	}

	record := PIDRecord{}
	if err := json.Unmarshal(data, &record); err != nil {
		return OrphanProcess{}, fmt.Errorf("provider.FindOrphans: decode pidfile %q: %w", path, err)
	}

	orphan := OrphanProcess{Path: path, StepID: stepIDFromPIDFile(path), Record: record}
	classifyOrphan(&orphan)

	return orphan, nil
}

func classifyOrphan(orphan *OrphanProcess) {
	if orphan.Record.PID <= 1 {
		orphan.StaleReason = "unsafe pid"

		return
	}

	if orphan.Record.PID == os.Getpid() {
		orphan.Alive = true
		orphan.StaleReason = "unsafe pid"

		return
	}

	if err := syscall.Kill(orphan.Record.PID, 0); err != nil {
		orphan.StaleReason = "process is not running"

		return
	}

	orphan.Alive = true

	matched, err := pidMatchesBinary(orphan.Record.PID, orphan.Record.Binary)
	if err != nil {
		orphan.StaleReason = err.Error()

		return
	}

	if !matched {
		orphan.StaleReason = "process identity mismatch"

		return
	}

	// Start-time check: only when StartedAt is recorded (old pidfiles without
	// it skip this check and rely on cmdline alone).
	if orphan.Record.StartedAt != "" {
		recorded, parseErr := time.Parse(time.RFC3339, orphan.Record.StartedAt)
		if parseErr != nil {
			orphan.StaleReason = fmt.Sprintf("parse started_at: %v", parseErr)

			return
		}

		live, startErr := procStartTime(orphan.Record.PID)
		if startErr != nil {
			// Cannot confirm identity; never kill on uncertainty.
			orphan.StaleReason = fmt.Sprintf("read process start time: %v", startErr)

			return
		}

		if !startTimeWithinTolerance(live, recorded) {
			orphan.StaleReason = "process start time mismatch (recycled pid)"

			return
		}
	}

	orphan.IdentityMatched = true
}

func reapConfirmedOrphan(orphan OrphanProcess) error {
	pid := orphan.Record.PID
	pgid := orphan.Record.PGID

	if pid <= 1 || pid == os.Getpid() || pgid <= 1 || pgid == os.Getpid() {
		return fmt.Errorf("provider.ReapOrphans: unsafe pid/pgid pid=%d pgid=%d", pid, pgid)
	}

	logSignalError(syscall.Kill(-pgid, syscall.SIGTERM), "SIGTERM", -pgid)
	waitForProcessExit(pid, DefaultExitGrace)

	if err := syscall.Kill(pid, 0); err == nil {
		logSignalError(syscall.Kill(-pgid, syscall.SIGKILL), "SIGKILL", -pgid)
		waitForProcessExit(pid, DefaultKillGrace)
	} else if !errors.Is(err, syscall.ESRCH) {
		logSignalError(err, "probe", pid)
	}

	return nil
}

func waitForProcessExit(pid int, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}

		time.Sleep(orphanProbeInterval)
	}
}

func pidMatchesBinary(pid int, binary string) (bool, error) {
	base := filepath.Base(strings.TrimSpace(binary))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return false, errors.New("recorded binary is empty")
	}

	data, err := os.ReadFile(filepath.Join(string(filepath.Separator)+"proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false, fmt.Errorf("read process cmdline: %w", err)
	}

	for field := range strings.SplitSeq(string(data), "\x00") {
		if filepath.Base(field) == base {
			return true, nil
		}
	}

	return false, nil
}

func stepIDFromPIDFile(path string) string {
	return filepath.Base(filepath.Dir(path))
}

// procStartTime returns the wall-clock start time of the process with the
// given pid by combining the system boot time from /proc/stat with the
// process start ticks from /proc/<pid>/stat.
//
// The comm field (field 2) in /proc/<pid>/stat can contain spaces and
// parentheses, so we locate the last ')' and split the remainder; starttime
// is overall field 22, i.e. index 19 of the fields after the closing paren.
func procStartTime(pid int) (time.Time, error) {
	btime, err := readBootTime()
	if err != nil {
		return time.Time{}, err
	}

	startTicks, err := readProcStartTicks(pid)
	if err != nil {
		return time.Time{}, err
	}

	startUnix := btime + startTicks/linuxClockTicksPerSecond

	return time.Unix(startUnix, 0), nil
}

// readBootTime parses the `btime <unixSeconds>` line from /proc/stat.
func readBootTime() (int64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, fmt.Errorf("read /proc/stat: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed btime line in /proc/stat: %q", line)
		}

		btime, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse btime %q: %w", fields[1], err)
		}

		return btime, nil
	}

	return 0, errors.New("btime not found in /proc/stat")
}

// readProcStartTicks returns the starttime field (field 22, index 19 after the
// closing paren of comm) from /proc/<pid>/stat as clock ticks since boot.
func readProcStartTicks(pid int) (int64, error) {
	path := filepath.Join(string(filepath.Separator)+"proc", strconv.Itoa(pid), "stat")

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	content := string(data)

	// Locate the last ')' to skip the comm field which may contain spaces/parens.
	lastParen := strings.LastIndex(content, ")")
	if lastParen < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat: no closing paren", pid)
	}

	// Fields after the closing paren are space-separated; starttime is index 19
	// (overall field 22, 0-indexed from after the paren).
	rest := strings.TrimSpace(content[lastParen+1:])
	fields := strings.Fields(rest)

	const starttimeIndex = 19
	if len(fields) <= starttimeIndex {
		return 0, fmt.Errorf("malformed /proc/%d/stat: too few fields after comm", pid)
	}

	ticks, err := strconv.ParseInt(fields[starttimeIndex], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse starttime from /proc/%d/stat: %w", pid, err)
	}

	return ticks, nil
}

// startTimeWithinTolerance reports whether the live process start time is
// within tolerance of the recorded start time. A 10-second window accounts
// for the small skew between fork and pidfile write.
func startTimeWithinTolerance(live, recorded time.Time) bool {
	const tolerance = 10 * time.Second

	diff := live.Sub(recorded)
	if diff < 0 {
		diff = -diff
	}

	return diff <= tolerance
}
