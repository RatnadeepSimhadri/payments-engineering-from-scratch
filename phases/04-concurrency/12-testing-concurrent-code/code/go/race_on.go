//go:build race

package main

// raceDetectorEnabled is true when the binary was built with -race. The stress test of RacyCounter is the one
// place in the course where a data race is the point, so the test skips it under the detector instead of failing.
const raceDetectorEnabled = true
