//go:build !linux && !darwin

package alina

import "errors"

func sandboxAvailable() bool { return false }
func sandboxCommand(shell string, args []string) (string, []string, error) {
	return "", nil, errors.New("network sandbox unavailable")
}
func sandboxExec(args []string) error { return errors.New("network sandbox unavailable") }
