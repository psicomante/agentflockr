//go:build unix

package process

import "syscall"

// Signal 0 checks existence and permission without delivering anything.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
