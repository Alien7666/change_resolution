package process

import "strings"

// Checker observes whether a process with a given executable name is running.
type Checker interface {
	Running(executableName string) (bool, error)
}

// matchesExecutable reports whether one process-snapshot entry names the watched
// executable. The comparison is exact and case-insensitive: Windows process names
// are case-insensitive, and a prefix or substring match would also fire on
// unrelated Riot executables.
func matchesExecutable(name, target string) bool {
	return strings.EqualFold(name, target)
}
