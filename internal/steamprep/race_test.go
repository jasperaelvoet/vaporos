//go:build race

package steamprep

// raceEnabled: the race detector slows the parser down many times, so
// timings mean nothing.
const raceEnabled = true
