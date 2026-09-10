//go:build !unix

package service

// dirWritable is never consulted off Unix: guided setup targets only the
// supported platforms.
func dirWritable(string) bool { return false }
