// Package process answers questions about other local processes.
package process

import (
	"errors"
	"fmt"
	"time"
)

// ErrUnsupported reports a question this platform cannot answer.
var ErrUnsupported = errors.New("not supported on this platform")

// Alive reports whether pid names a running process this user may signal.
// A process owned by another user counts as not running: callers look up
// pids that the same user's tools recorded, so such a pid was reused.
func Alive(pid int) bool {
	return pid > 0 && alive(pid)
}

// StartTime returns when pid started.
func StartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, fmt.Errorf("no process %d", pid)
	}
	return startTime(pid)
}
