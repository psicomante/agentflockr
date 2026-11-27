//go:build !darwin && !linux

package process

import "time"

func startTime(int) (time.Time, error) { return time.Time{}, ErrUnsupported }
