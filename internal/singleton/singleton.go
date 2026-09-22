// Package singleton refuses a second copy of the tool on one desktop.
//
// Two copies would each keep their own saved arrangement and their own watcher, and
// the second to apply a mode would record the first one's changed desktop as the
// "original" to restore to. Nothing about that is recoverable by the user: whichever
// copy exits last puts back an arrangement that was never theirs.
package singleton

import "errors"

// ErrAlreadyRunning means another copy holds the name. It is not a failure the user
// needs to act on beyond looking at the window that is already open, so callers report
// it and exit cleanly rather than logging a fatal error.
var ErrAlreadyRunning = errors.New("另一個執行個體已在執行中")
