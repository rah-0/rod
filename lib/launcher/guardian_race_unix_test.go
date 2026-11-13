//go:build race && !windows

package launcher

import (
	"slices"
	"testing"
)

func TestGuardianRaceEnvironment(t *testing.T) {
	const quiet = "atexit_sleep_ms=0"
	for _, test := range []struct {
		name string
		env  []string
		// The entry that guardianRaceEnv appends, or "" when it keeps env.
		added string
	}{
		{"no GORACE", []string{"HOME=/home/user"}, "GORACE=" + quiet},
		{"empty GORACE", []string{"GORACE="}, "GORACE=" + quiet},
		{"other options", []string{"GORACE=halt_on_error=1 history_size=2"}, "GORACE=halt_on_error=1 history_size=2 " + quiet},
		{"owner sets the delay", []string{"GORACE=atexit_sleep_ms=250"}, ""},
		{"owner disables the delay", []string{"GORACE=halt_on_error=1 atexit_sleep_ms=0"}, ""},
		{"comma separator", []string{"GORACE=halt_on_error=1,atexit_sleep_ms=250"}, ""},
		{"colon separator", []string{"GORACE=halt_on_error=1:atexit_sleep_ms=250"}, ""},
		{"tab separator", []string{"GORACE=halt_on_error=1\tatexit_sleep_ms=250"}, ""},
		{"line break separator", []string{"GORACE=halt_on_error=1\r\natexit_sleep_ms=250"}, ""},
		// The race runtime reads these as values of other options.
		{"option name inside a value", []string{"GORACE=log_path=/tmp/atexit_sleep_ms=250"}, "GORACE=log_path=/tmp/atexit_sleep_ms=250 " + quiet},
		{"option inside a quoted value", []string{"GORACE=log_path='/tmp/a atexit_sleep_ms=250'"}, "GORACE=log_path='/tmp/a atexit_sleep_ms=250' " + quiet},
		{"longer option name", []string{"GORACE=no_atexit_sleep_ms=250"}, "GORACE=no_atexit_sleep_ms=250 " + quiet},
		// A quoted value ends at its closing mark, and only a mark that
		// starts a value quotes it.
		{"option after a quoted value", []string{`GORACE=log_path="/tmp/a b"atexit_sleep_ms=250`}, ""},
		{"quotation mark inside a value", []string{"GORACE=log_path=/tmp/it's atexit_sleep_ms=250"}, ""},
		{"other variable", []string{"GORACE_COPY=atexit_sleep_ms=250", "XGORACE=atexit_sleep_ms=250"}, "GORACE=" + quiet},
		// As when the process starts, the last entry is the one in effect.
		{"last entry lacks the option", []string{"GORACE=atexit_sleep_ms=250", "HOME=/home/user", "GORACE=halt_on_error=1"}, "GORACE=halt_on_error=1 " + quiet},
		{"last entry sets the option", []string{"GORACE=halt_on_error=1", "GORACE=atexit_sleep_ms=250"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := slices.Clone(test.env)
			if test.added != "" {
				want = append(want, test.added)
			}
			if got := guardianRaceEnv(slices.Clone(test.env)); !slices.Equal(got, want) {
				t.Fatalf("guardianRaceEnv(%q) = %q, want %q", test.env, got, want)
			}
		})
	}
}
