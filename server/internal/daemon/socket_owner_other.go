//go:build !linux && !darwin

package daemon

func socketOwnerPID(string) int {
	return 0
}
