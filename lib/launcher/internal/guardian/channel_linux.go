package guardian

import (
	"errors"
	"io"
	"os"
	"strconv"
	"syscall"
)

func parentChannel(file *os.File) bool {
	peer, err := syscall.GetsockoptUcred(int(file.Fd()), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil || peer.Pid != int32(os.Getppid()) || peer.Uid != uint32(os.Geteuid()) || peer.Gid != uint32(os.Getegid()) {
		return false
	}
	// A caller that merely knows the private markers must not be able to
	// impersonate the launcher. The channel must come from this executable.
	self, err := os.Stat("/proc/self/exe")
	if err != nil {
		return false
	}
	parent, err := os.Stat("/proc/" + strconv.Itoa(int(peer.Pid)) + "/exe")
	return err == nil && os.SameFile(self, parent)
}

// authenticateParent requires a fresh response from the actual parent PID.
// SO_PEERCRED alone identifies the socket's creator, even if it later execs
// this executable and leaves the socket with an unrelated process.
func authenticateParent(status *os.File) error {
	if !parentChannel(os.Stdin) {
		return errors.New("guardian parent identity changed")
	}
	if err := syscall.SetsockoptInt(0, syscall.SOL_SOCKET, syscall.SO_PASSCRED, 1); err != nil {
		return err
	}
	entropy, err := os.Open("/dev/urandom")
	if err != nil {
		return err
	}
	if err := validateEntropyDevice(entropy); err != nil {
		_ = entropy.Close()
		return err
	}
	var challenge [32]byte
	_, err = io.ReadFull(entropy, challenge[:])
	_ = entropy.Close()
	if err != nil {
		return err
	}
	if _, err := status.Write(challenge[:]); err != nil {
		return err
	}
	var response [32]byte
	for offset := 0; offset < len(response); {
		control := make([]byte, syscall.CmsgSpace(syscall.SizeofUcred))
		n, controlSize, flags, _, err := syscall.Recvmsg(0, response[offset:], control, 0)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.EOF
		}
		if flags&syscall.MSG_CTRUNC != 0 {
			return errors.New("guardian parent credentials truncated")
		}
		messages, err := syscall.ParseSocketControlMessage(control[:controlSize])
		if err != nil || len(messages) != 1 {
			return errors.New("guardian parent credentials missing")
		}
		peer, err := syscall.ParseUnixCredentials(&messages[0])
		if err != nil || peer.Pid != int32(os.Getppid()) || peer.Uid != uint32(os.Geteuid()) || peer.Gid != uint32(os.Getegid()) {
			return errors.New("guardian challenge answered by another process")
		}
		offset += n
	}
	if response != challenge || !parentChannel(os.Stdin) {
		return errors.New("guardian parent authentication failed")
	}
	return nil
}

// AnswerChallenge responds from the launching process itself. The kernel
// records its credentials on this message; a forked delegate cannot answer.
func AnswerChallenge(status, command *os.File) error {
	var challenge [32]byte
	if _, err := io.ReadFull(status, challenge[:]); err != nil {
		return err
	}
	n, err := syscall.SendmsgN(int(command.Fd()), challenge[:], nil, nil, 0)
	if err == nil && n != len(challenge) {
		return io.ErrShortWrite
	}
	return err
}

// Validate the opened descriptor, not the pathname: a substituted regular file
// or another device must not supply a predictable parent challenge. Linux's
// urandom character device has major 1, minor 9 on every supported architecture.
func validateEntropyDevice(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeCharDevice == 0 || uint64(stat.Rdev) != 0x109 {
		return errors.New("guardian requires the kernel urandom device")
	}
	return nil
}
