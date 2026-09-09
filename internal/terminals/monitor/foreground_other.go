//go:build !linux && !darwin

package monitor

// Unsupported platforms observe nothing: the foreground group is read but
// never named, so terminals keep their creation-time fallback.

func readProcess(int) (process, bool) { return process{}, false }

func groupMembers(int) []int { return nil }
