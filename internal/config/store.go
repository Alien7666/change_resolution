package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EnvPath overrides the whole configuration path — the file, not the directory it
// sits in. Tests and development builds point it at a scratch file, which is what
// keeps the test suite away from the user's real settings.
const EnvPath = "RESOLUTION_TRAY_CONFIG"

const (
	appDirectory = "ResolutionTray"
	fileName     = "config.json"

	// The temporary file is the destination plus this suffix so it always lands in
	// the same directory: a rename across volumes is not atomic.
	tempSuffix = ".tmp"

	backupStampLayout = "20060102-150405"
	maxBackupAttempts = 100
)

// renameFile is os.Rename behind a seam so a test can fail the last step of a save
// and prove the previous configuration survived it. Nothing else replaces it.
var renameFile = os.Rename

// writeMu serialises everything that writes. The temporary file's name is derived
// from the destination rather than randomised — the spec fixes it at
// config.json.tmp — so two concurrent saves would otherwise share one temp file and
// rename a torn mixture of both over the user's settings. Save and Backup are the
// only writers, and they take this lock for the whole of their work.
//
// This covers one process. A second copy of the tool running as the same user is
// not guarded here and is not guarded anywhere else in the tool either.
var writeMu sync.Mutex

// Path reports where the configuration lives.
//
// %APPDATA%\ResolutionTray\config.json rather than a file beside the executable: a
// single-file release gets dropped anywhere, Program Files is not writable without
// elevation this tool does not ask for, and UAC virtualisation would silently
// redirect the write to VirtualStore — leaving the user editing one file while the
// tool reads another.
func Path() (string, error) {
	if override := os.Getenv(EnvPath); strings.TrimSpace(override) != "" {
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("找不到設定檔資料夾：%w", err)
	}
	return filepath.Join(base, appDirectory, fileName), nil
}

// Load reads and validates the configuration. It writes nothing on any path,
// including success: a file that is missing a new optional field is not rewritten to
// add it, and a file that could not be understood is left exactly as the user wrote
// it.
//
// A missing file is not a failure the user has to repair — it is first run — so the
// returned error keeps fs.ErrNotExist intact for the caller to test with errors.Is.
func Load() (File, error) {
	path, err := Path()
	if err != nil {
		return File{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("讀取設定檔 %s 失敗：%w", path, err)
	}
	file, err := Parse(data)
	if err != nil {
		return File{}, fmt.Errorf("%s：%w", path, err)
	}
	return file, nil
}

// Save validates and then writes the configuration atomically.
//
// It refuses anything Load would refuse, so the tool can never leave a document on
// disk that it would reject on the next start, and a refusal writes nothing at all.
func Save(file File) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	path, err := Path()
	if err != nil {
		return err
	}
	settled := file.normalized()
	if settled.Version != Version {
		return fmt.Errorf("拒絕寫出 version %d 的設定檔：這個版本只寫 version %d", settled.Version, Version)
	}
	if err := validate(settled); err != nil {
		return err
	}
	data, err := Marshal(settled)
	if err != nil {
		return err
	}
	return writeAtomically(path, data)
}

// Backup renames a configuration the tool could not understand to
// config.bad-<timestamp>.json and reports where it went.
//
// It is only ever called because the user chose 重新設定. Reading a broken file never
// moves it: a file the user is in the middle of editing must still be where they
// left it.
func Backup() (string, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	path, err := Path()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("沒有可保留的設定檔：%w", err)
	}

	directory := filepath.Dir(path)
	extension := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), extension)
	stamp := time.Now().Format(backupStampLayout)

	for attempt := 0; attempt < maxBackupAttempts; attempt++ {
		name := fmt.Sprintf("%s.bad-%s%s", base, stamp, extension)
		if attempt > 0 {
			name = fmt.Sprintf("%s.bad-%s-%d%s", base, stamp, attempt+1, extension)
		}
		target := filepath.Join(directory, name)
		// Two rejections in the same second must not let the second one overwrite
		// the evidence the first one kept. The name is claimed with an exclusive
		// create rather than checked with Stat: a check followed by a rename leaves
		// a window in which the name is taken between the two.
		claim, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("保留設定檔 %s 失敗：%w", path, err)
		}
		claim.Close()
		if err := renameFile(path, target); err != nil {
			// The placeholder is this call's own, so removing it cannot discard
			// anybody else's backup.
			os.Remove(target)
			return "", fmt.Errorf("保留設定檔 %s 失敗：%w", path, err)
		}
		return target, nil
	}
	return "", fmt.Errorf("保留設定檔 %s 失敗：同一秒內已經有 %d 份備份", path, maxBackupAttempts)
}

// writeAtomically writes through a temporary file in the destination's own directory
// and renames it over the target. The destination is never opened for truncation:
// losing power midway through that leaves a zero-length settings file, which is
// worse than losing the change.
func writeAtomically(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("建立設定檔資料夾 %s 失敗：%w", directory, err)
	}

	temporary := path + tempSuffix
	handle, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("建立暫存設定檔 %s 失敗：%w", temporary, err)
	}
	if _, err := handle.Write(data); err != nil {
		handle.Close()
		os.Remove(temporary)
		return fmt.Errorf("寫入暫存設定檔 %s 失敗：%w", temporary, err)
	}
	if err := handle.Sync(); err != nil {
		handle.Close()
		os.Remove(temporary)
		return fmt.Errorf("同步暫存設定檔 %s 失敗：%w", temporary, err)
	}
	if err := handle.Close(); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("關閉暫存設定檔 %s 失敗：%w", temporary, err)
	}
	if err := renameFile(temporary, path); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("寫入設定檔 %s 失敗：%w", path, err)
	}
	return nil
}
