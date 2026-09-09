package alina

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var packageCommand = regexp.MustCompile(`(?i)(^|[^a-z0-9_-])(pkg|apt|apt-get|pip|pip3|npm|pnpm|yarn|bun|brew|port|dnf|yum|apk|gem|cargo|go|uv|nix)\s+(-[^\s]+\s+)*(install|add|upgrade|update|reinstall|remove|uninstall|sync|tool|dlx|get|fetch)\b|(^|[^a-z0-9_-])(dpkg|pacman|npx)\s`)
var networkCommand = regexp.MustCompile(`(?i)(https?://|ftp://|(^|[^a-z0-9_-])(curl|wget|aria2c|ssh|scp|sftp|rsync|nc|ncat|netcat|socat|telnet|ftp)(\s|["'])|git\s+(clone|fetch|pull|push|ls-remote))`)

func shellAction(command, dir string, network bool) Action {
	a := Action{Tool: "shell", Command: command, Directory: dir, Network: network}
	// Conservative POC classifier. The kernel network filter also applies to
	// interpreters and scripts. Offline package manipulation is not sandboxed.
	if packageCommand.MatchString(command) {
		a.Reason = "Package manager or language installer command"
		a.Network = true
	}
	if network || networkCommand.MatchString(command) {
		a.Network = true
		if a.Reason != "" {
			a.Reason += "; "
		}
		a.Reason += "Network access / possible download"
	}
	if !sandboxAvailable() {
		a.Network = true
		a.Reason = "OS network sandbox unavailable: unrestricted shell requires consent"
	}
	return a
}

type cappedBuffer struct {
	mu        sync.Mutex
	b         bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := (48 << 10) - b.b.Len()
	if len(p) > left {
		p = p[:left]
		b.truncated = true
	}
	b.b.Write(p)
	return n, nil
}
func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := strings.ToValidUTF8(b.b.String(), "�")
	if b.truncated {
		s += "\n[output truncated at 48 KiB]"
	}
	return s
}
func shellEnvironment() []string {
	// Keep only ordinary execution settings; never inherit provider credentials.
	keys := []string{"PATH", "HOME", "ALINA_HOME", "XDG_CONFIG_HOME", "PREFIX", "TMPDIR", "LANG", "LC_ALL", "TERM", "SHELL", "LD_LIBRARY_PATH", "ANDROID_ROOT", "ANDROID_DATA", "EXTERNAL_STORAGE",
		// Termux:API launches Android's app_process through am. ART needs its
		// platform paths/classpath even though arbitrary daemon secrets stay out.
		"ANDROID_ART_ROOT", "ANDROID_RUNTIME_ROOT", "ANDROID_I18N_ROOT", "ANDROID_TZDATA_ROOT", "BOOTCLASSPATH", "DEX2OATBOOTCLASSPATH"}
	env := []string{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}
func runShell(ctx context.Context, a Action, timeout int) (string, error) {
	if len(a.Command) == 0 || len(a.Command) > 32000 {
		return "", errors.New("command must be 1-32000 bytes")
	}
	if !filepath.IsAbs(a.Directory) {
		return "", errors.New("absolute working directory required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	shell, e := exec.LookPath("sh")
	if e != nil {
		return "", e
	}
	path, args := shell, []string{"-c", a.Command}
	if !a.Network {
		path, args, e = sandboxCommand(shell, args)
		if e != nil {
			return "", e
		}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = a.Directory
	cmd.Env = shellEnvironment()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	var out cappedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	e = cmd.Start()
	if e != nil {
		return "", e
	}
	pid := cmd.Process.Pid
	e = cmd.Wait()
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	code := 0
	if e != nil {
		code = -1
		var ee *exec.ExitError
		if errors.As(e, &ee) {
			code = ee.ExitCode()
		}
	}
	result := fmt.Sprintf("exit_code: %d\n%s", code, out.String())
	if ctx.Err() != nil {
		result += "\n" + ctx.Err().Error()
	}
	return result, nil
}

// The declared policy is a trusted-agent contract, not OS enforcement of file
// downloads. Strict policy remains available where network isolation is needed.
func declaredAction(command, dir string, network, download, install bool) Action {
	a := Action{Tool: "shell", Command: command, Directory: dir, Network: network || networkCommand.MatchString(command)}
	if install || packageCommand.MatchString(command) {
		a.Network = true
		a.Reason = "Package installation or modification"
	}
	if download || downloadCommand.MatchString(command) {
		a.Network = true
		if a.Reason != "" {
			a.Reason += "; "
		}
		a.Reason += "File download"
	}
	// Platforms without a filter run local commands normally under this policy.
	if !sandboxAvailable() {
		a.Network = true
	}
	return a
}

var downloadCommand = regexp.MustCompile(`(?i)(\b(wget|aria2c|scp|sftp)\b|git\s+(clone|fetch|pull)\b|curl\b[^\n]*(\s-[a-z]*[oO]|--output|--remote-name|\s>))`)
