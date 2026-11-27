package process

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

func startTime(pid int) (time.Time, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, err
	}
	if int(info.Proc.P_pid) != pid {
		return time.Time{}, fmt.Errorf("no process %d", pid)
	}
	started := info.Proc.P_starttime
	return time.Unix(started.Sec, int64(started.Usec)*int64(time.Microsecond)), nil
}
