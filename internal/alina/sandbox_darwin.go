package alina

import (
	"errors"
	"os"
)

func sandboxAvailable() bool { _, e := os.Stat("/usr/bin/sandbox-exec"); return e == nil }
func sandboxCommand(shell string, args []string) (string, []string, error) {
	return "/usr/bin/sandbox-exec", append([]string{"-p", "(version 1) (allow default) (deny network*)", shell}, args...), nil
}
func sandboxExec(args []string) error { return errors.New("internal sandbox entry is Linux-only") }
