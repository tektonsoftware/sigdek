//go:build race

package rules

// raceDetector: timings mean little under the race detector, which slows code ten times or more.
const raceDetector = true
