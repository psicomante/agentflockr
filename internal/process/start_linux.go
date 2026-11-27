package process

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// startTime adds field 22 of /proc/PID/stat, the start in clock ticks
// after boot, to the boot time in /proc/stat.
func startTime(pid int) (time.Time, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, err
	}
	// The command name, field 2, may contain spaces and parentheses; the
	// fields after its closing parenthesis start with field 3.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return time.Time{}, fmt.Errorf("unexpected /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return time.Time{}, fmt.Errorf("unexpected /proc/%d/stat", pid)
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	system, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for line := range strings.Lines(string(system)) {
		if boot, ok := strings.CutPrefix(line, "btime "); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(boot), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			// USER_HZ, the unit of the tick count, is 100 on every
			// architecture Go supports.
			return time.Unix(seconds, 0).Add(time.Duration(ticks) * time.Second / 100), nil
		}
	}
	return time.Time{}, fmt.Errorf("no boot time in /proc/stat")
}
