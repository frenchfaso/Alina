package alina

import "errors"

func delegateSandboxAvailable() bool { return sandboxAvailable() }
func delegateSandboxCommand(root, command string) (string, []string, error) {
	// Apple's system.sb uses this exact literal rule to obtain cwd. It matches
	// the root directory itself, not its descendants (unlike subpath "/").
	const cwdRule = `(allow file-read* file-test-existence (literal "/"))`
	profile := `(version 1)(deny default)(allow process-exec process-fork)(allow sysctl-read)(allow file-read-metadata)(allow file-read* (subpath "/System") (subpath "/usr") (subpath "/bin") (subpath "/sbin") (subpath "/Library/Apple") (subpath "/opt/homebrew"))(allow file-read* file-write* (literal "/dev/null") (literal "/dev/zero") (literal "/dev/urandom"))(allow file-read* file-write* (subpath ` + jsonText(root) + `))`
	return "/usr/bin/sandbox-exec", []string{"-p", profile + cwdRule, "/bin/sh", "-c", command}, nil
}
func delegateSandboxExec([]string) error {
	return errors.New("internal delegate sandbox helper is Linux-only")
}
