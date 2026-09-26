//go:build linux

package alina

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func delegateSandboxAvailable() bool {
	v, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	return e == 0 && v >= 3 && sandboxAvailable()
}
func delegateSandboxCommand(root, command string) (string, []string, error) {
	if !delegateSandboxAvailable() {
		return "", nil, errors.New("delegate filesystem sandbox unavailable")
	}
	binary, err := os.Executable()
	return binary, []string{"__delegate_shell", root, command}, err
}
func delegateSandboxExec(args []string) error {
	if len(args) != 2 || !delegateSandboxAvailable() {
		return errors.New("delegate sandbox unavailable")
	}
	runtime.LockOSThread()
	root, err := filepath.EvalSymlinks(args[0])
	if err != nil {
		return err
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		return err
	}
	if err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	// ABI 3 handles truncation as well as all earlier filesystem operations.
	attr := struct{ FS uint64 }{0x7fff}
	fd, _, eno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if eno != 0 {
		return eno
	}
	defer unix.Close(int(fd))
	add := func(path string, access uint64) error {
		p, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer unix.Close(p)
		// The kernel ABI struct is packed, with the FD immediately after access.
		rule := struct {
			Access uint64
			Parent int32
		}{access, int32(p)}
		_, _, errNo := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, fd, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
		if errNo != 0 {
			return errNo
		}
		return nil
	}
	const read = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_EXECUTE
	for _, p := range []string{"/usr/bin", "/usr/lib", "/usr/lib64", "/bin", "/lib", "/lib64", "/system", "/apex"} {
		if err = add(p, read); err != nil {
			return err
		}
	}
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		for _, d := range []string{"bin", "lib", "share"} {
			if err = add(filepath.Join(prefix, d), read); err != nil {
				return err
			}
		}
	}
	if err = add(root, 0x7fff&^(unix.LANDLOCK_ACCESS_FS_MAKE_CHAR|unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK|unix.LANDLOCK_ACCESS_FS_MAKE_SOCK)); err != nil {
		return err
	}
	for _, p := range []string{"/dev/null", "/dev/zero", "/dev/urandom", "/dev/random"} {
		if err = add(p, unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_WRITE_FILE); err != nil {
			return err
		}
	}
	_, _, eno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0)
	if eno != 0 {
		return eno
	}
	arch := uint32(unix.AUDIT_ARCH_AARCH64)
	if runtime.GOARCH == "amd64" {
		arch = unix.AUDIT_ARCH_X86_64
	}
	f := []unix.SockFilter{{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4}, {Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch, Jt: 1}, {Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS}, {Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}}
	if runtime.GOARCH == "amd64" {
		f = append(f, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: 0x40000000, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS})
	}
	for _, n := range []uint32{unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD, unix.SYS_PIDFD_SEND_SIGNAL, unix.SYS_KILL, unix.SYS_TKILL, unix.SYS_TGKILL, unix.SYS_RT_SIGQUEUEINFO, unix.SYS_RT_TGSIGQUEUEINFO, unix.SYS_IO_URING_SETUP, unix.SYS_SETSID, unix.SYS_SETPGID, unix.SYS_MOUNT, unix.SYS_UNSHARE, unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_BPF} {
		f = append(f, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: n, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)})
	}
	f = append(f, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	prog := unix.SockFprog{Len: uint16(len(f)), Filter: &f[0]}
	_, _, eno = unix.Syscall(unix.SYS_PRCTL, unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)))
	runtime.KeepAlive(f)
	if eno != 0 {
		return eno
	}
	return syscall.Exec(shell, []string{shell, "-c", args[1]}, os.Environ())
}
