package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// pointConfigAt redirects the whole store into the test's own directory. Every test
// in this package calls it: the user's real %APPDATA%\ResolutionTray\config.json is
// never read and never written by the test suite.
func pointConfigAt(t *testing.T, path string) {
	t.Helper()
	t.Setenv(EnvPath, path)
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s) = %v", dir, err)
	}
	names := make([]string, 0, len(found))
	for _, entry := range found {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func TestPathUsesTheUserConfigDirectoryByDefault(t *testing.T) {
	pointConfigAt(t, "")

	got, err := Path()
	if err != nil {
		t.Fatalf("Path() = %v", err)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("os.UserConfigDir() unavailable: %v", err)
	}
	want := filepath.Join(base, "ResolutionTray", "config.json")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

// The override is the whole path, not a directory to join something onto: tests and
// development builds point it at a scratch file and expect exactly that file.
func TestPathPrefersTheEnvironmentOverrideVerbatim(t *testing.T) {
	want := filepath.Join(t.TempDir(), "odd name.json")
	pointConfigAt(t, want)

	got, err := Path()
	if err != nil {
		t.Fatalf("Path() = %v", err)
	}
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestSaveWritesTheDocumentAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	if err := Save(FromProfile(roundTripProfile())); err != nil {
		t.Fatalf("Save = %v", err)
	}

	want, err := Marshal(FromProfile(roundTripProfile()))
	if err != nil {
		t.Fatalf("Marshal = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile = %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("file content =\n%s\nwant\n%s", got, want)
	}
	if names := entries(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("directory holds %v, want only config.json (no surviving .tmp)", names)
	}
}

// The temp file has to sit in the destination's own directory: a rename across
// volumes is not atomic, which is the entire reason for writing this way. This
// asserts the shape of the write rather than only its outcome.
func TestSaveRenamesACompleteTemporaryFileFromTheSameDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	var from, to string
	var staged []byte
	renameFile = func(source, target string) error {
		from, to = source, target
		content, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		staged = content
		return os.Rename(source, target)
	}
	t.Cleanup(func() { renameFile = os.Rename })

	if err := Save(FromProfile(roundTripProfile())); err != nil {
		t.Fatalf("Save = %v", err)
	}

	if want := path + ".tmp"; from != want {
		t.Errorf("renamed from %q, want %q", from, want)
	}
	if filepath.Dir(from) != dir {
		t.Errorf("the temp file lived in %s, want the destination directory %s", filepath.Dir(from), dir)
	}
	if to != path {
		t.Errorf("renamed to %q, want %q", to, path)
	}
	// The bytes were already complete before the rename, so the rename is the only
	// moment at which the configuration changes.
	if _, err := Parse(staged); err != nil {
		t.Errorf("the staged temp file did not parse: %v", err)
	}
}

func TestSaveCreatesTheConfigDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ResolutionTray", "config.json")
	pointConfigAt(t, path)

	if err := Save(FromProfile(roundTripProfile())); err != nil {
		t.Fatalf("Save = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(%s) = %v", path, err)
	}
}

func TestSavedFileRoundTripsThroughLoad(t *testing.T) {
	pointConfigAt(t, filepath.Join(t.TempDir(), "config.json"))

	profile := roundTripProfile()
	if err := Save(FromProfile(profile)); err != nil {
		t.Fatalf("Save = %v", err)
	}
	file, err := Load()
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if got := file.Profile(); !reflect.DeepEqual(got, profile) {
		t.Errorf("Load().Profile() = %+v, want %+v", got, profile)
	}
}

// The write is temp-then-rename precisely so that a failure at the last step leaves
// the user's previous configuration untouched rather than truncated.
func TestSaveKeepsThePreviousConfigWhenTheRenameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	previous := roundTripProfile()
	if err := Save(FromProfile(previous)); err != nil {
		t.Fatalf("Save(previous) = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile = %v", err)
	}

	renameFile = func(string, string) error { return errors.New("rename refused") }
	t.Cleanup(func() { renameFile = os.Rename })

	next := roundTripProfile()
	next.GameMode.Width = 1280
	next.GameMode.Height = 960
	if err := Save(FromProfile(next)); err == nil {
		t.Fatal("Save = nil, want the rename failure to surface")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after the failed save = %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("the previous config changed:\n%s\nwant\n%s", after, before)
	}
	if _, err := Parse(after); err != nil {
		t.Errorf("the previous config no longer parses: %v", err)
	}
	if names := entries(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("directory holds %v, want only config.json (the temp file must be cleaned up)", names)
	}
}

// The temporary file's name is derived from the destination, so two saves at once
// would share it. Whatever ends up on disk must still be one of the configurations
// that was saved, never a mixture of them.
func TestConcurrentSavesNeverLeaveATornFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	wide := roundTripProfile()
	narrow := roundTripProfile()
	narrow.GameMode.Width = 1280
	narrow.GameMode.Height = 960

	var group sync.WaitGroup
	for i := range 16 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			profile := wide
			if i%2 == 1 {
				profile = narrow
			}
			if err := Save(FromProfile(profile)); err != nil {
				t.Errorf("Save = %v", err)
			}
		}(i)
	}
	group.Wait()

	file, err := Load()
	if err != nil {
		t.Fatalf("Load after concurrent saves = %v", err)
	}
	if got := file.Profile(); !reflect.DeepEqual(got, wide) && !reflect.DeepEqual(got, narrow) {
		t.Errorf("Load().Profile() = %+v, want either %+v or %+v", got, wide, narrow)
	}
	if names := entries(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("directory holds %v, want only config.json", names)
	}
}

// Save is held to the same rules as Load: a document the tool would refuse to read
// is never put on disk, and refusing it writes nothing at all.
func TestSaveRefusesToWriteAFileItWouldRefuseToLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	broken := FromProfile(roundTripProfile())
	broken.GameMode.RefreshHz = 1

	if err := Save(broken); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("Save = %v, want ErrOutOfRange", err)
	}
	if names := entries(t, dir); len(names) != 0 {
		t.Errorf("directory holds %v, want nothing written", names)
	}
}

func TestSaveRefusesAFileThatIsNotTheCurrentVersion(t *testing.T) {
	dir := t.TempDir()
	pointConfigAt(t, filepath.Join(dir, "config.json"))

	stale := FromProfile(roundTripProfile())
	stale.Version = Version + 1

	if err := Save(stale); err == nil {
		t.Fatal("Save = nil, want a refusal to write a version this build does not own")
	}
	if names := entries(t, dir); len(names) != 0 {
		t.Errorf("directory holds %v, want nothing written", names)
	}
}

// This is the whole "no partial loading, no repair" rule, asserted directly: a file
// the tool cannot understand is left exactly as the user wrote it.
func TestLoadWritesNothingWhenTheFileIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	const broken = "{\n  \"version\": 1,\n  \"gameMode\": {\"width\": 0}\n}\n"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat = %v", err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("Load = nil, want a rejection")
	}

	if names := entries(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("directory holds %v, want only the original config.json", names)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after Load = %v", err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Errorf("mod time changed from %v to %v", info.ModTime(), after.ModTime())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile = %v", err)
	}
	if string(content) != broken {
		t.Errorf("content changed to\n%s", content)
	}
}

func TestLoadWritesNothingWhenThePathCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "config.json")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatalf("Mkdir = %v", err)
	}
	pointConfigAt(t, unreadable)

	if _, err := Load(); err == nil {
		t.Fatal("Load = nil, want an I/O rejection")
	}
	if names := entries(t, unreadable); len(names) != 0 {
		t.Errorf("the path now holds %v, want nothing written", names)
	}
}

// A missing file is not an error the user has to fix — it is first run. The caller
// separates the two with errors.Is, so Load has to keep fs.ErrNotExist intact.
func TestLoadReportsAMissingFileAsNotExist(t *testing.T) {
	pointConfigAt(t, filepath.Join(t.TempDir(), "config.json"))

	_, err := Load()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load = %v, want an error matching fs.ErrNotExist", err)
	}
}

func TestBackupRenamesTheUnreadableFileAndReturnsTheNewPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	const broken = "{ this is not json\n"
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}

	moved, err := Backup()
	if err != nil {
		t.Fatalf("Backup = %v", err)
	}
	if filepath.Dir(moved) != dir {
		t.Errorf("Backup wrote to %s, want a file in %s", moved, dir)
	}
	base := filepath.Base(moved)
	if !strings.HasPrefix(base, "config.bad-") || !strings.HasSuffix(base, ".json") {
		t.Errorf("Backup produced %q, want config.bad-<timestamp>.json", base)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the original file is still there: %v", err)
	}
	kept, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("ReadFile(%s) = %v", moved, err)
	}
	if string(kept) != broken {
		t.Errorf("the kept copy reads %q, want %q", kept, broken)
	}
}

func TestBackupDoesNotOverwriteAnEarlierBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	first := "first\n"
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}
	firstMoved, err := Backup()
	if err != nil {
		t.Fatalf("Backup(first) = %v", err)
	}

	second := "second\n"
	if err := os.WriteFile(path, []byte(second), 0o600); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}
	secondMoved, err := Backup()
	if err != nil {
		t.Fatalf("Backup(second) = %v", err)
	}
	if secondMoved == firstMoved {
		t.Fatalf("both backups landed on %s", firstMoved)
	}
	kept, err := os.ReadFile(firstMoved)
	if err != nil {
		t.Fatalf("ReadFile(%s) = %v", firstMoved, err)
	}
	if string(kept) != first {
		t.Errorf("the first backup now reads %q, want %q", kept, first)
	}
}

func TestBackupReportsThereIsNothingToKeep(t *testing.T) {
	pointConfigAt(t, filepath.Join(t.TempDir(), "config.json"))

	if _, err := Backup(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Backup = %v, want an error matching fs.ErrNotExist", err)
	}
}

// Keeping a copy of a file the tool could not understand is an explicit user action
// ("重新設定"), never something reading it does on its own.
func TestLoadNeverBacksAFileUpOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	pointConfigAt(t, path)

	if err := os.WriteFile(path, []byte("{ not json\n"), 0o600); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load = nil, want a rejection")
	}
	for _, name := range entries(t, dir) {
		if strings.Contains(name, "bad-") {
			t.Errorf("Load created %s on its own", name)
		}
	}
}
