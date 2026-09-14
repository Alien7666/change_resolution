package process

import (
	"sort"
	"strings"
)

// Checker observes whether a process with a given executable name is running.
type Checker interface {
	Running(executableName string) (bool, error)
}

// Lister lists the executable names present in a process snapshot.
type Lister interface {
	Names() ([]string, error)
}

// ToolhelpChecker is the read-only process observer returned by
// NewToolhelpChecker. It retains both the existing Checker behavior and the
// process-name listing used by the settings picker.
type ToolhelpChecker interface {
	Checker
	Lister
}

func uniqueSortedNames(names []string) []string {
	var unique []string
	for _, name := range names {
		if name == "" {
			continue
		}
		duplicate := false
		for _, existing := range unique {
			if strings.EqualFold(existing, name) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			unique = append(unique, name)
		}
	}

	sort.SliceStable(unique, func(i, j int) bool {
		return strings.ToLower(unique[i]) < strings.ToLower(unique[j])
	})
	return unique
}

// matchesExecutable reports whether one process-snapshot entry names the watched
// executable. The comparison is exact and case-insensitive: Windows process names
// are case-insensitive, and a prefix or substring match would also fire on
// unrelated Riot executables.
func matchesExecutable(name, target string) bool {
	return strings.EqualFold(name, target)
}
