//go:build race

package launcher

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Only the guardian's own environment receives the race detector option. The
// browser keeps the environment that the launcher configured for it.
func TestGuardianProcessEnvironment(t *testing.T) {
	for _, test := range []struct{ name, owner, guardian, browser string }{
		// An empty owner value stands for an unset variable.
		{"unset", "", "GORACE=atexit_sleep_ms=0", ""},
		{"other options", "history_size=1", "GORACE=history_size=1 atexit_sleep_ms=0", "GORACE=history_size=1"},
		{"owner sets the delay", "atexit_sleep_ms=1", "GORACE=atexit_sleep_ms=1", "GORACE=atexit_sleep_ms=1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Setenv restores the owner's variable after the subtest.
			t.Setenv("GORACE", test.owner)
			if test.owner == "" {
				if err := os.Unsetenv("GORACE"); err != nil {
					t.Fatal(err)
				}
			}
			// The browser stand-in exits by itself after a minute.
			cmd := guardianTestCommand(t, "leaf", t.TempDir())
			cmd.Args[1] = "-test.run=^TestGuardianTreeProcess$"
			pid, stop, err := New().startProcess(cmd)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				stop()
				_ = cmd.Wait()
			})
			// startProcess replaced cmd with the guardian's command.
			if got := guardianTestEnviron(t, cmd.Process.Pid, "GORACE"); got != test.guardian {
				t.Fatalf("guardian environment has %q, want %q", got, test.guardian)
			}
			if got := guardianTestEnviron(t, pid, "GORACE"); got != test.browser {
				t.Fatalf("browser environment has %q, want %q", got, test.browser)
			}
		})
	}
}

// guardianTestEnviron returns the entries for name in the environment that the
// process started with, joined by spaces, or "" when it has none.
func guardianTestEnviron(t *testing.T, pid int, name string) string {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for entry := range strings.SplitSeq(string(data), "\x00") {
		if strings.HasPrefix(entry, name+"=") {
			entries = append(entries, entry)
		}
	}
	return strings.Join(entries, " ")
}
