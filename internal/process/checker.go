package process

import "strings"

// Checker observes whether a process with a given executable name is running.
type Checker interface {
	Running(executableName string) (bool, error)
}

func containsExecutable(names []string, target string) bool {
	for _, name := range names {
		if strings.EqualFold(name, target) {
			return true
		}
	}
	return false
}
