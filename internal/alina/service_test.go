package alina

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetachedServiceLifecycle(t *testing.T) {
	dir, err := os.MkdirTemp("", "alina-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	c := DefaultConfig()
	c.WorkDir = dir
	c.Memory.Dream = false
	c.Autonomy.Enabled = false
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	run := func(args ...string) (map[string]any, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, self, append([]string{"__cli"}, args...)...)
		cmd.Env = append(os.Environ(), "ALINA_HOME="+dir)
		output, err := cmd.CombinedOutput()
		var obj map[string]any
		json.Unmarshal(output, &obj)
		return obj, string(output), err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		stopDaemon(ctx, dir)
	}()
	first, output, err := run("serve")
	if err != nil || first["running"] != true {
		t.Fatal(output, err)
	}
	pid := first["pid"]
	status, err := daemonStatus(context.Background(), dir)
	if err != nil || status["pid"] != pid {
		t.Fatal("child did not survive launcher exit", status, err)
	}
	second, output, err := run("serve")
	if err != nil || second["already_running"] != true {
		t.Fatal("start not idempotent", output, err)
	}
	after, output, err := run("serve", "restart")
	if err != nil || after["pid"] == pid || after["running"] != true {
		t.Fatal("restart failed", output, err)
	}
	for i := 0; i < 2; i++ {
		v, output, err := run("serve", "stop")
		if err != nil || v["running"] != false {
			t.Fatal("stop not idempotent", output, err)
		}
	}
	if _, err := daemonStatus(context.Background(), dir); err == nil {
		t.Fatal("daemon still reachable")
	}
	if info, err := os.Stat(filepath.Join(dir, "logs", "daemon.log")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private diagnostic log", err)
	}
}

func TestDaemonStartupFailureIsReported(t *testing.T) {
	dir, err := os.MkdirTemp("", "alina-fail-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	c := DefaultConfig()
	c.WorkDir = dir
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	// Configuration is valid but binding the socket must fail in the child.
	if err := os.WriteFile(socketPath(dir), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "__cli", "serve")
	cmd.Env = append(os.Environ(), "ALINA_HOME="+dir)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "non-socket") {
		t.Fatal(string(output), err)
	}
	lock, err := lockDaemonState(dir)
	if err != nil {
		t.Fatal("failed child still holds lock", err)
	}
	lock.Close()
	b, _ := os.ReadFile(socketPath(dir))
	if string(b) != "keep" {
		t.Fatal("occupied socket path was modified")
	}
}
