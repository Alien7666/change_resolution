package process

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type toolhelpChecker struct{}

// NewToolhelpChecker creates a read-only process snapshot checker.
func NewToolhelpChecker() ToolhelpChecker {
	return &toolhelpChecker{}
}

func (c *toolhelpChecker) Running(target string) (bool, error) {
	found := false
	err := c.walk(func(name string) bool {
		if matchesExecutable(name, target) {
			found = true
			return false
		}
		return true
	})
	return found, err
}

func (c *toolhelpChecker) Names() ([]string, error) {
	var names []string
	err := c.walk(func(name string) bool {
		names = append(names, name)
		return true
	})
	if err != nil {
		return nil, err
	}
	return uniqueSortedNames(names), nil
}

func (c *toolhelpChecker) walk(visit func(name string) bool) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	if snapshot == windows.InvalidHandle {
		return fmt.Errorf("CreateToolhelp32Snapshot: returned invalid handle")
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		return fmt.Errorf("Process32First: %w", err)
	}
	for {
		if !visit(windows.UTF16ToString(entry.ExeFile[:])) {
			return nil
		}
		err := windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("Process32Next: %w", err)
		}
	}
}
