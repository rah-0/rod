//go:build !windows

package guardian

import (
	"errors"
	"io"
	"strconv"
	"syscall"
)

// Messages between the launcher and its supervisor are lists of strings: a
// 4-byte big-endian payload size, then for each string a 4-byte big-endian
// length and its bytes. The launcher starts its own executable as the
// supervisor, so the format carries no version. A reader consumes exactly one
// message, so the bytes that follow it stay in the pipe. A payload holds at
// most maxMessage bytes.
const maxMessage = 1 << 30

var errMessage = errors.New("launcher guardian: malformed message")

func writeMessage(w io.Writer, fields ...string) error {
	size := 0
	for _, field := range fields {
		// Compare before adding: the sum of the lengths can exceed an int.
		if len(field) > maxMessage-size-4 {
			return errors.New("launcher guardian: message too large")
		}
		size += 4 + len(field)
	}
	message := make([]byte, 4, 4+size)
	putUint32(message, uint32(size))
	for _, field := range fields {
		message = append(message, 0, 0, 0, 0)
		putUint32(message[len(message)-4:], uint32(len(field)))
		message = append(message, field...)
	}
	_, err := w.Write(message)
	return err
}

func readMessage(r io.Reader) ([]string, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := uint32At(header[:])
	if size > maxMessage {
		return nil, errMessage
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	var fields []string
	for len(payload) > 0 {
		if len(payload) < 4 {
			return nil, errMessage
		}
		length := uint32At(payload)
		payload = payload[4:]
		if uint64(length) > uint64(len(payload)) {
			return nil, errMessage
		}
		fields = append(fields, string(payload[:length]))
		payload = payload[length:]
	}
	return fields, nil
}

func putUint32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func uint32At(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// WriteCommand sends c to the supervisor.
func WriteCommand(w io.Writer, c Command) error {
	fields := make([]string, 0, 7+len(c.Args)+len(c.Env)+len(c.Scratch))
	fields = append(fields, c.Path, c.Dir, c.Preferences, c.Profile)
	for _, list := range [][]string{c.Args, c.Env, c.Scratch} {
		fields = append(fields, strconv.Itoa(len(list)))
		fields = append(fields, list...)
	}
	return writeMessage(w, fields...)
}

// ReadCommand receives the command written by [WriteCommand]. Its lists are
// never nil.
func ReadCommand(r io.Reader) (Command, error) {
	fields, err := readMessage(r)
	if err != nil {
		return Command{}, err
	}
	if len(fields) < 4 {
		return Command{}, errMessage
	}
	c := Command{Path: fields[0], Dir: fields[1], Preferences: fields[2], Profile: fields[3]}
	fields = fields[4:]
	for _, list := range []*[]string{&c.Args, &c.Env, &c.Scratch} {
		if len(fields) == 0 {
			return Command{}, errMessage
		}
		count, err := strconv.Atoi(fields[0])
		if err != nil || count < 0 || count > len(fields)-1 {
			return Command{}, errMessage
		}
		*list = append(make([]string, 0, count), fields[1:1+count]...)
		fields = fields[1+count:]
	}
	if len(fields) != 0 {
		return Command{}, errMessage
	}
	return c, nil
}

// WriteStatus sends s to the launcher.
func WriteStatus(w io.Writer, s Status) error {
	ready := "0"
	if s.Ready {
		ready = "1"
	}
	return writeMessage(w, ready, strconv.Itoa(s.PID), strconv.FormatUint(uint64(s.Errno), 10), s.Error, s.Op, s.Path)
}

// ReadStatus receives the status written by [WriteStatus].
func ReadStatus(r io.Reader) (Status, error) {
	fields, err := readMessage(r)
	if err != nil {
		return Status{}, err
	}
	if len(fields) != 6 || (fields[0] != "0" && fields[0] != "1") {
		return Status{}, errMessage
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return Status{}, errMessage
	}
	errno, err := strconv.ParseUint(fields[2], 10, 0)
	if err != nil {
		return Status{}, errMessage
	}
	return Status{
		PID:   pid,
		Error: fields[3],
		Errno: syscall.Errno(errno),
		Op:    fields[4],
		Path:  fields[5],
		Ready: fields[0] == "1",
	}, nil
}
