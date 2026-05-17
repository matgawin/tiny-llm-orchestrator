package launcher

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tiny-llm-orchestrator/orc/internal/runstore"
	"tiny-llm-orchestrator/orc/internal/stableerr"
)

const processTerminateGrace = 25 * time.Millisecond

func terminateProcessGroup(pid int) {
	if pid <= 0 {
		return
	}

	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		time.Sleep(processTerminateGrace)
	}

	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func attemptStillStarting(attempt runstore.Attempt, now time.Time) bool {
	if attempt.State != runstore.AttemptStateStarting || attempt.PID != 0 {
		return false
	}

	return !attemptTimedOut(attempt, now)
}

func attemptTimedOut(attempt runstore.Attempt, now time.Time) bool {
	timeout, err := time.ParseDuration(attempt.Timeout)
	if err != nil || timeout <= 0 {
		return false
	}

	return now.Sub(attempt.StartedAt) > timeout
}

func processIdentityMatches(pid int, wantStartTime string) bool {
	if wantStartTime == "" {
		return false
	}

	gotStartTime, err := processStartIdentity(pid)
	if err != nil {
		return false
	}

	return gotStartTime == wantStartTime
}

func processStartIdentity(pid int) (string, error) {
	if runtime.GOOS != "linux" {
		return "", stableerr.Errorf("process identity requires linux procfs, got %s", runtime.GOOS)
	}

	content, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // #nosec G304 -- pid is numeric and scoped to procfs.
	if err != nil {
		return "", fmt.Errorf("read process identity for pid %d: %w", pid, err)
	}

	return parseProcStatStartTime(string(content))
}

func parseProcStatStartTime(stat string) (string, error) {
	end := strings.LastIndex(stat, ") ")
	if end == -1 {
		return "", stableerr.New("parse process identity: missing command field")
	}

	fields := strings.Fields(stat[end+2:])

	const startTimeIndexAfterCommand = 19
	if len(fields) <= startTimeIndexAfterCommand {
		return "", stableerr.New("parse process identity: missing starttime field")
	}

	if _, err := strconv.ParseUint(fields[startTimeIndexAfterCommand], 10, 64); err != nil {
		return "", fmt.Errorf("parse process identity starttime: %w", err)
	}

	return fields[startTimeIndexAfterCommand], nil
}
