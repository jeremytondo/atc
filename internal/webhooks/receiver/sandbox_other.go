//go:build !linux

package receiver

import "errors"

// enforce has no sandbox to offer off Linux; ingress never gets this far,
// but the receiver stays honest if run by hand.
func enforce(string, int) (abi int, permanent bool, err error) {
	return 0, true, errors.New("webhook ingress requires Linux: the receiver is isolated with Landlock")
}

func selfTest(Options) (map[string]string, []string) {
	return nil, []string{"no sandbox on this platform"}
}
