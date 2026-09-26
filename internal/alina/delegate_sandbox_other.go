//go:build !linux && !darwin

package alina

import "errors"

func delegateSandboxAvailable() bool { return false }
func delegateSandboxCommand(string, string) (string, []string, error) {
	return "", nil, errors.New("delegate shell unavailable on this OS")
}
func delegateSandboxExec([]string) error { return errors.New("delegate shell unavailable on this OS") }
