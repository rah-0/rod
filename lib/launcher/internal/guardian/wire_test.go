//go:build !windows

package guardian

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestCommandRoundTrip(t *testing.T) {
	for _, command := range []Command{
		{Args: []string{}, Env: []string{}, Scratch: []string{}},
		{
			Path:        "/opt/browser/chrome",
			Args:        []string{"chrome", "", "--flag=a b", "--json={\"a\":[1,2]}\n", "3"},
			Env:         []string{"A=1", "EMPTY=", "UTF8=é\U0001f600", "BINARY=\x00\xff"},
			Dir:         "relative/dir",
			Preferences: `{"profile":{"name":"x"}}`,
			Profile:     "Profile 1",
			Scratch:     []string{"rod-AAAAAAAA", "rod-BBBBBBBB"},
		},
		{Path: "p", Args: []string{"1", "2"}, Env: []string{}, Scratch: []string{"0"}},
	} {
		var buffer bytes.Buffer
		if err := WriteCommand(&buffer, command); err != nil {
			t.Fatal(err)
		}
		buffer.WriteString("liveness")
		got, err := ReadCommand(&buffer)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, command) {
			t.Fatalf("command = %#v, want %#v", got, command)
		}
		if rest := buffer.String(); rest != "liveness" {
			t.Fatalf("reader consumed bytes after the command: %q remain", rest)
		}
	}
}

// A nil environment would make the browser inherit the supervisor's own.
func TestCommandListsAreNeverNil(t *testing.T) {
	var buffer bytes.Buffer
	if err := WriteCommand(&buffer, Command{}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCommand(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got.Args == nil || got.Env == nil || got.Scratch == nil {
		t.Fatalf("decoded lists: %#v", got)
	}
}

func TestStatusRoundTrip(t *testing.T) {
	missing := &os.PathError{Op: "fork/exec", Path: "/missing/browser", Err: syscall.ENOENT}
	var failed Status
	failed.SetError(missing)
	for _, status := range []Status{{Ready: true}, {PID: 4242}, failed, {Error: "plain\nfailure"}} {
		var buffer bytes.Buffer
		if err := WriteStatus(&buffer, status); err != nil {
			t.Fatal(err)
		}
		got, err := ReadStatus(&buffer)
		if err != nil {
			t.Fatal(err)
		}
		if got != status {
			t.Fatalf("status = %#v, want %#v", got, status)
		}
	}
	var pathError *os.PathError
	if err := failed.Err(); !errors.Is(err, os.ErrNotExist) || !errors.As(err, &pathError) || *pathError != *missing {
		t.Fatalf("rebuilt error = %#v", err)
	}
}

func TestMalformedMessages(t *testing.T) {
	var command bytes.Buffer
	if err := WriteCommand(&command, Command{Path: "p", Args: []string{"a"}, Env: []string{"B=1"}, Scratch: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	whole := command.Bytes()
	if _, err := ReadCommand(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("empty input: %v, want EOF", err)
	}
	for size := 1; size < len(whole); size++ {
		if _, err := ReadCommand(bytes.NewReader(whole[:size])); err != io.ErrUnexpectedEOF {
			t.Fatalf("%d of %d bytes: %v, want unexpected EOF", size, len(whole), err)
		}
	}
	for name, message := range map[string]string{
		"oversized":            "\xff\xff\xff\xff",
		"truncated length":     "\x00\x00\x00\x02\x00\x00",
		"field past the end":   "\x00\x00\x00\x05\x00\x00\x00\x02a",
		"no fields":            "\x00\x00\x00\x00",
		"list count too large": frame("p", "", "", "", "2", "a"),
		"negative list count":  frame("p", "", "", "", "-1"),
		"missing lists":        frame("p", "", "", "", "0", "0"),
		"trailing field":       frame("p", "", "", "", "0", "0", "0", "extra"),
	} {
		if _, err := ReadCommand(strings.NewReader(message)); err != errMessage {
			t.Errorf("%s command: %v", name, err)
		}
	}
	for name, message := range map[string]string{
		"too few fields": frame("1", "0", "0", "", ""),
		"ready flag":     frame("yes", "0", "0", "", "", ""),
		"process ID":     frame("0", "x", "0", "", "", ""),
		"error number":   frame("0", "0", "-1", "", "", ""),
	} {
		if _, err := ReadStatus(strings.NewReader(message)); err != errMessage {
			t.Errorf("%s status: %v", name, err)
		}
	}
}

// A message holds at most 1 GiB. The writer refuses a larger one before it
// writes anything, and the reader refuses the size before it reads further.
func TestMessageSizeLimit(t *testing.T) {
	// The same string many times: the message would exceed the limit by about
	// 4 KiB.
	arguments := slices.Repeat([]string{strings.Repeat("x", 1<<20)}, 1<<10)
	var buffer bytes.Buffer
	err := WriteCommand(&buffer, Command{Args: arguments})
	if err == nil || err.Error() != "launcher guardian: message too large" || buffer.Len() != 0 {
		t.Fatalf("oversized command: %v, %d bytes written", err, buffer.Len())
	}
	reader := strings.NewReader("\x40\x00\x00\x01rest")
	if _, err := ReadCommand(reader); err != errMessage || reader.Len() != len("rest") {
		t.Fatalf("oversized size: %v, %d bytes unread", err, reader.Len())
	}
}

func frame(fields ...string) string {
	var buffer bytes.Buffer
	if err := writeMessage(&buffer, fields...); err != nil {
		panic(err)
	}
	return buffer.String()
}
