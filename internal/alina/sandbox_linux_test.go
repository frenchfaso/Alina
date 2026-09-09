//go:build linux

package alina

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestTermuxCameraInfoWithShellEnvironment(t *testing.T) {
	if runtime.GOOS != "android" || os.Getenv("ALINA_TEST_TERMUX_CAMERA") != "1" {
		t.Skip("opt-in Termux:API camera metadata check; no photo is taken")
	}
	out, err := runShell(context.Background(), Action{Command: "termux-camera-info", Directory: t.TempDir()}, 15)
	start, end := strings.Index(out, "\n["), strings.LastIndex(out, "]")
	if err != nil || !strings.HasPrefix(out, "exit_code: 0\n") || start < 0 || end <= start {
		t.Fatal("camera metadata failed in the harness shell", out, err)
	}
	var cameras []struct{ ID, Facing string }
	if err := json.Unmarshal([]byte(out[start+1:end+1]), &cameras); err != nil || len(cameras) == 0 {
		t.Fatal("camera metadata missing or invalid", err)
	}
	t.Logf("Termux:API returned %d cameras through the filtered shell", len(cameras))
}

func TestSandboxUnixIPC(t *testing.T) {
	if !sandboxAvailable() {
		t.Skip("unsupported seccomp architecture")
	}
	if address := os.Getenv("ALINA_TEST_IPC_ADDRESS"); address != "" {
		// Run in a fresh process: seccomp must not affect the test runner.
		for _, family := range []int{unix.AF_INET, unix.AF_INET6, unix.AF_NETLINK, unix.AF_PACKET} {
			fd, err := unix.Socket(family, unix.SOCK_DGRAM, 0)
			if fd >= 0 {
				unix.Close(fd)
			}
			if !errors.Is(err, unix.EPERM) {
				t.Fatalf("socket family %d was not denied: %v", family, err)
			}
		}
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal("local socketpair", err)
		}
		defer unix.Close(pair[0])
		defer unix.Close(pair[1])
		if _, err := unix.Write(pair[0], []byte("ok")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2)
		if n, err := unix.Read(pair[1], buf); err != nil || n != 2 || string(buf) != "ok" {
			t.Fatal("local socketpair transfer", n, err)
		}
		listener, err := net.Listen("unix", address)
		if err != nil {
			t.Fatal("local listener", err)
		}
		defer listener.Close()
		listener.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
		fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		if err := unix.Connect(fd, &unix.SockaddrUnix{Name: address}); !errors.Is(err, unix.EPERM) {
			t.Fatal("outbound Unix connect was not denied", err)
		}
		fmt.Println("IPC_READY")
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ok" {
			t.Fatal("local app result could not be received", err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	address := "@alina-test-" + randomID()
	cmd := exec.CommandContext(ctx, executable, "__sandbox", executable, "-test.run=^TestSandboxUnixIPC$")
	cmd.Env = append(os.Environ(), "ALINA_TEST_IPC_ADDRESS="+address)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cmd.Wait() }()
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || line != "IPC_READY\n" {
		io.Copy(io.Discard, reader)
		cmd.Wait()
		t.Fatal("sandbox probe failed", line, err, stderr.String())
	}
	conn, err := net.DialTimeout("unix", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Write([]byte("ok"))
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	output, _ := io.ReadAll(reader)
	if err := cmd.Wait(); err != nil {
		t.Fatal("sandbox probe failed", err, string(output), stderr.String())
	}
}
