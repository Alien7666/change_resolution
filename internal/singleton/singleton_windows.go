//go:build windows

package singleton

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// Local\ scopes the name to the logon session, so a second user signed in at the
	// same time gets their own desktop and their own copy rather than being refused by
	// someone else's. The desktop this tool rearranges is per-session too, which is
	// what makes that the right boundary.
	namePrefix = `Local\`

	swRestore = 9
)

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW     = user32.NewProc("FindWindowW")
	procShowWindow      = user32.NewProc("ShowWindow")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	procIsIconic        = user32.NewProc("IsIconic")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
)

// Acquire takes the name for this process. The returned release closes the handle;
// Windows drops the name on its own when the process ends, so release exists for
// orderly shutdown rather than for correctness.
//
// The mutex is never waited on and never signalled. It is used only for the fact that
// creating it twice is detectable, which is the one thing a lock file on a crashed
// process cannot promise.
func Acquire(name string) (release func(), err error) {
	wide, err := windows.UTF16PtrFromString(namePrefix + name)
	if err != nil {
		return nil, fmt.Errorf("singleton: encode name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, false, wide)
	// CreateMutex returns a valid handle even when the name already exists, and the
	// error is how the caller learns which case it is. The handle must still be closed.
	if err == windows.ERROR_ALREADY_EXISTS {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, fmt.Errorf("singleton: create mutex: %w", err)
	}
	return func() { _ = windows.CloseHandle(handle) }, nil
}

// ActivateExisting brings the copy that is already running to the front, so a user who
// launched the tool a second time sees its window instead of nothing at all. A tray
// application spends most of its life hidden, and "nothing happened" is exactly what a
// silent refusal looks like from the outside.
//
// Every failure here is ignorable: the other copy is running either way, and it may
// legitimately have no window at this instant. The caller exits regardless.
func ActivateExisting(title string) {
	wide, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(wide)))
	if hwnd == 0 {
		return
	}
	minimized, _, _ := procIsIconic.Call(hwnd)
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if minimized != 0 || visible == 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procSetForeground.Call(hwnd)
}
