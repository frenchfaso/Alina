package alina

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func serveCLI(ctx context.Context, dir string, args []string, out io.Writer) error {
	if len(args) == 1 && args[0] == "--foreground" {
		return Serve(ctx, dir, Config{})
	}
	action := "start"
	if len(args) == 1 {
		action = args[0]
	}
	if len(args) > 1 || (action != "start" && action != "stop" && action != "restart") {
		return errors.New("usage: alina serve [stop|restart|--foreground]")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "service-control.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("another service operation is in progress")
	}
	if action == "stop" || action == "restart" {
		if err = stopDaemon(ctx, dir); err != nil {
			return err
		}
		if action == "stop" {
			return printJSON(out, map[string]any{"running": false})
		}
	}
	return startDaemon(ctx, dir, out)
}

func daemonStatus(ctx context.Context, dir string) (map[string]any, error) {
	check, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var status map[string]any
	err := localRequest(check, dir, "GET", "/v1/status", nil, &status)
	return status, err
}
func startDaemon(ctx context.Context, dir string, out io.Writer) error {
	if status, err := daemonStatus(ctx, dir); err == nil {
		return printJSON(out, map[string]any{"running": true, "already_running": true, "pid": status["pid"]})
	}
	c, err := LoadConfig(dir)
	if err != nil {
		return configError(err)
	}
	if err = c.Validate(); err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	logs, err := daemonOutput(dir)
	if err != nil {
		return err
	}
	defer logs.Close()
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer read.Close()
	defer write.Close()
	cmd := exec.Command(binary, "__daemon")
	cmd.Env = append(os.Environ(), "ALINA_HOME="+dir)
	cmd.Dir = "/"
	cmd.Stdin = nil
	cmd.Stdout = logs
	cmd.Stderr = logs
	cmd.ExtraFiles = []*os.File{write}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	write.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(io.LimitReader(read, 4096)).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	started := false
	defer func() {
		if !started {
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-exited
			}
		}
	}()
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case line := <-ready:
		if line != "ready" {
			if line == "" {
				line = "daemon exited before readiness; inspect logs/daemon.log"
			}
			return errors.New(line)
		}
	case <-wait.Done():
		return fmt.Errorf("daemon startup did not complete: %w", wait.Err())
	}
	status, err := daemonStatus(ctx, dir)
	if err != nil {
		return fmt.Errorf("daemon readiness check failed: %w", err)
	}
	if status["pid"] != float64(cmd.Process.Pid) {
		return errors.New("daemon readiness identity mismatch")
	}
	started = true
	return printJSON(out, map[string]any{"running": true, "pid": cmd.Process.Pid})
}

func stopDaemon(ctx context.Context, dir string) error {
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	var result map[string]any
	err := localRequest(check, dir, "POST", "/v1/service/stop", nil, &result)
	cancel()
	if err != nil {
		lock, lockErr := lockDaemonState(dir)
		if lockErr == nil {
			lock.Close()
			return nil
		}
		return fmt.Errorf("cannot stop the running instance through its local API: %w", err)
	}
	wait, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		lock, lockErr := lockDaemonState(dir)
		if lockErr == nil {
			lock.Close()
			return nil
		}
		if !errors.Is(lockErr, errStateLocked) {
			return lockErr
		}
		select {
		case <-wait.Done():
			return errors.New("shutdown still in progress; inspect alina status and logs")
		case <-ticker.C:
		}
	}
}

func daemonOutput(dir string) (*os.File, error) {
	path := filepath.Join(dir, "logs")
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if info, err := root.Lstat("daemon.log"); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("daemon.log must be a regular file")
		}
		if info.Size() > logFileBytes {
			if err = root.Rename("daemon.log", "daemon.previous.log"); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := root.OpenFile("daemon.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("daemon.log must be a regular file")
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
