package process

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type toolhelpChecker struct{}

// NewToolhelpChecker creates a read-only process snapshot checker.
func NewToolhelpChecker() Checker {
	return &toolhelpChecker{}
}

func (c *toolhelpChecker) Running(target string) (bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	if snapshot == windows.InvalidHandle {
		return false, fmt.Errorf("CreateToolhelp32Snapshot: returned invalid handle")
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return false, nil
		}
		return false, fmt.Errorf("Process32First: %w", err)
	}
	for {
		if matchesExecutable(windows.UTF16ToString(entry.ExeFile[:]), target) {
			return true, nil
		}
		err := windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("Process32Next: %w", err)
		}
	}
}
