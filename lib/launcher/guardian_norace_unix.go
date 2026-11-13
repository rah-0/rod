//go:build !race && !windows

package launcher

func guardianRaceEnv(env []string) []string { return env }
