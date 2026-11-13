//go:build race && !windows

package launcher

import "strings"

// guardianRaceEnv keeps the race detector from delaying the guardian's exit.
// A race-enabled program sleeps at exit, one second by default, while other
// goroutines are alive, and the guardian always has one watching its owner.
// That sleep would delay every browser cleanup, which waits for the guardian.
// It only gives other goroutines time to run into a race: a race is reported
// as soon as it is detected. A delay that the owner sets in GORACE is kept.
func guardianRaceEnv(env []string) []string {
	const name, option = "GORACE=", "atexit_sleep_ms"
	options := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, name); ok {
			options = value
		}
	}
	if raceOptionSet(options, option) {
		return env
	}
	// A later entry replaces earlier ones when the process starts.
	return append(env, name+strings.TrimSpace(options+" "+option+"=0"))
}

// raceOptionSet reports whether options, a GORACE value, sets the named
// option. It reads the value as the race runtime does: spaces, commas, colons,
// tabs, and line breaks separate options, and a value that starts with a
// quotation mark ends at the next such mark, whatever is between them.
func raceOptionSet(options, name string) bool {
	const separators = " ,:\t\n\r"
	for {
		option, value, found := strings.Cut(strings.TrimLeft(options, separators), "=")
		if !found {
			return false
		}
		if option == name {
			return true
		}
		if value != "" && (value[0] == '\'' || value[0] == '"') {
			_, options, _ = strings.Cut(value[1:], value[:1])
		} else if end := strings.IndexAny(value, separators); end >= 0 {
			options = value[end:]
		} else {
			return false
		}
	}
}
