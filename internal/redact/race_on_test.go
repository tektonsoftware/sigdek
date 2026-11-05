//go:build race

package redact

// raceDetector: timings mean little under the race detector, which slows code ten times or more.
const raceDetector = true
