//go:build linux

package alina

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func sandboxAvailable() bool { return runtime.GOARCH == "arm64" || runtime.GOARCH == "amd64" }
func sandboxCommand(shell string, args []string) (string, []string, error) {
	p, e := os.Executable()
	return p, append([]string{"__sandbox", shell}, args...), e
}
func sandboxExec(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("missing sandbox command")
	}
	if !sandboxAvailable() {
		return fmt.Errorf("unsupported seccomp architecture")
	}
	runtime.LockOSThread()
	arch := uint32(unix.AUDIT_ARCH_AARCH64)
	if runtime.GOARCH == "amd64" {
		arch = unix.AUDIT_ARCH_X86_64
	}
	f := []unix.SockFilter{{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4}, {Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch, Jt: 1}, {Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS}, {Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}}
	if runtime.GOARCH == "amd64" {
		f = append(f, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: 0x40000000, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS})
	}
	for _, n := range []uint32{unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_PTRACE, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_IO_URING_SETUP} {
		f = append(f, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: n, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)})
	}
	f = append(f, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	if e := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); e != nil {
		return fmt.Errorf("no_new_privs: %w", e)
	}
	prog := unix.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, _, errno := syscall.RawSyscall6(unix.SYS_PRCTL, unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0, 0)
	runtime.KeepAlive(f)
	if errno != 0 {
		return fmt.Errorf("seccomp: %w", errno)
	}
	return syscall.Exec(args[0], args, os.Environ())
}
