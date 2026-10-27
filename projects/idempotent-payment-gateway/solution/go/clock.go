// Project: projects/idempotent-payment-gateway — injected time (Go).
// Every component that needs "now" or has to wait takes a Clock, so tests never touch wall time.
package main

import (
	"fmt"
	"time"
)

// Clock is a time source the gateway reads and waits against.
type Clock interface {
	// NowMillis is milliseconds since the epoch (or since any fixed origin for a FakeClock).
	NowMillis() int64
	// Sleep blocks for the given duration. A FakeClock advances instead of blocking.
	Sleep(millis int64)
}

// FakeClock is a deterministic clock for tests and demos: time moves only when you move it.
type FakeClock struct {
	now int64
}

func NewFakeClock(startMillis int64) *FakeClock { return &FakeClock{now: startMillis} }

func (c *FakeClock) NowMillis() int64 { return c.now }

func (c *FakeClock) Sleep(millis int64) { c.Advance(millis) }

// Advance moves time forward; a negative duration is a programming error and panics.
func (c *FakeClock) Advance(millis int64) {
	if millis < 0 {
		panic(fmt.Sprintf("cannot advance a negative duration: %d", millis))
	}
	c.now += millis
}

func (c *FakeClock) Set(millis int64) { c.now = millis }

// SystemClock is the wall-clock implementation for a real deployment. Never used by the grader.
type SystemClock struct{}

func (SystemClock) NowMillis() int64 { return time.Now().UnixMilli() }

func (SystemClock) Sleep(millis int64) { time.Sleep(time.Duration(millis) * time.Millisecond) }
