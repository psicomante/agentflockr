package provider

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A command that leaves a grandchild holding its output pipe must not make
// Run wait for the grandchild: the dashboard's scan holds the engine lock
// meanwhile.
func TestRunDoesNotWaitForAGrandchildHoldingThePipe(t *testing.T) {
	old := waitDelay
	waitDelay = 200 * time.Millisecond
	defer func() { waitDelay = old }()

	start := time.Now()
	out, err := Run(context.Background(), "sh", "-c", "sleep 30 & echo done")
	if err != nil {
		t.Fatalf("a command that succeeded must not fail: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "done" {
		t.Errorf("output %q, want %q", got, "done")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Run waited %s for the grandchild", elapsed)
	}
}

// Standard error is kept only up to a limit, and a command that fills it is
// neither stopped nor reported as failing for that.
func TestRunKeepsOnlyTheStartOfStandardError(t *testing.T) {
	_, err := Run(context.Background(), "sh", "-c", "yes boom | head -c 5000000 >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("the command's failure and the start of its error output must be reported: %v", err)
	}
	b := NewHeadBuffer(8)
	if n, err := b.Write([]byte("0123456789")); n != 10 || err != nil {
		t.Fatalf("a full buffer must still accept writes: %d %v", n, err)
	}
	if n, err := b.Write([]byte("more")); n != 4 || err != nil {
		t.Fatalf("a full buffer must still accept writes: %d %v", n, err)
	}
	if got := b.String(); got != "01234567" {
		t.Errorf("kept %q, want the first 8 bytes", got)
	}
}
