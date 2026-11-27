package process

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("the current process is not alive")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if Alive(cmd.Process.Pid) {
		t.Fatal("an exited and reaped process is alive")
	}
	for _, pid := range []int{0, -1} {
		if Alive(pid) {
			t.Fatalf("pid %d is alive", pid)
		}
	}
}

func TestStartTime(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("start times need macOS or Linux")
	}
	own, err := StartTime(os.Getpid())
	if err != nil || own.After(time.Now()) || time.Since(own) > 24*time.Hour {
		t.Fatalf("this process started at %v (%v)", own, err)
	}
	// Linux counts in hundredths of a second from a boot time in whole
	// seconds, so allow a second either way.
	before := time.Now()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	started, err := StartTime(child.Process.Pid)
	if err != nil || started.Before(before.Add(-time.Second)) || started.After(time.Now().Add(time.Second)) {
		t.Fatalf("child started at %v, between %v and now (%v)", started, before, err)
	}
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{0, -1, exited.Process.Pid} {
		if _, err := StartTime(pid); err == nil {
			t.Fatalf("pid %d has a start time", pid)
		}
	}
}
