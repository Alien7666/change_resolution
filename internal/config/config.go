// Package config reads and writes the profile the user owns.
//
// The file lives outside the executable's directory (see store.go) and is edited by
// hand often enough that it is treated as untrusted input: every value is checked,
// and two rules hold everywhere in this package.
//
//   - A configuration is understood completely and used, or it is not used at all.
//     There is no partial load: every rejection returns the zero File, so nothing
//     downstream can accidentally run on half a configuration.
//   - A value outside its range is rejected, never clamped. Clamping rewrites what
//     the user meant without telling them. Nothing here ever repairs or rewrites a
//     file it could not understand.
//
// The user is also the author of the file, so every rejection names the field — and
// a syntax error names the line and column — because the fix is theirs to make.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Alien7666/change_resolution/internal/domain"
)

// Version is the schema version this build writes and is the highest it reads.
//
// Adding an optional field does not raise it: an absent optional field means the
// documented default, and supplying that default never causes the file to be
// rewritten. Only a change that gives an existing file a different meaning — a new
// unit for a field, or more than one profile per file — raises it.
const Version = 1

const (
	defaultBitsPerPixel        = 32
	defaultRestoreDelaySeconds = 3

	// 0 and 1 mean "whatever the hardware defaults to" in DEVMODE, so neither can be
	// something a user picked out of an enumerated list of modes.
	minRefreshHz = 2
	maxRefreshHz = 1000

	maxRestoreDelaySeconds = 60
	maxProcessNameLength   = 255
)

var (
	// ErrMalformed reports that the bytes are not a configuration file at all: bad
	// JSON, a value of the wrong JSON type, or a missing or nonsensical version.
	ErrMalformed = errors.New("設定檔格式錯誤")

	// ErrNewerVersion reports a file written by a later build. The tool goes
	// read-only and must never write: rewriting it as this version's schema would be
	// data loss dressed up as compatibility.
	ErrNewerVersion = errors.New("設定檔是較新版本寫的")

	// ErrMissingField reports that a required field is absent.
	ErrMissingField = errors.New("設定檔缺少必要欄位")

	// ErrOutOfRange reports a field the tool understands but refuses to act on.
	ErrOutOfRange = errors.New("設定檔的值超出範圍")
)

// Monitor is the stored half of domain.MonitorIdentity. \\.\DISPLAYn is deliberately
// not here: Windows assigns it dynamically, so it is re-resolved, never stored.
type Monitor struct {
	InstancePath   string `json:"instancePath"`
	HardwareID     string `json:"hardwareId"`
	ModelWasUnique bool   `json:"modelWasUnique"`
	Label          string `json:"label"`
}

type Mode struct {
	Width        uint32 `json:"width"`
	Height       uint32 `json:"height"`
	RefreshHz    uint32 `json:"refreshHz"`
	BitsPerPixel uint32 `json:"bitsPerPixel"`
}

// Watch is the optional automatic-restore half of the profile. An empty ProcessName
// is a legitimate configuration meaning "watch nothing, manual only"; the UI has to
// say so out loud so nobody believes an automatic restore is armed when it is not.
type Watch struct {
	ProcessName         string `json:"processName"`
	RestoreDelaySeconds uint32 `json:"restoreDelaySeconds"`
}

// File is a configuration that has already been understood: every optional field
// carries its resolved value rather than a maybe-absent one, so holding a File means
// holding something that passed validation.
//
// Parse is the only way to build one from bytes. Decoding JSON straight into this
// type would skip the presence checks and the validation that make that true.
type File struct {
	Version  int     `json:"version"`
	Monitor  Monitor `json:"monitor"`
	GameMode Mode    `json:"gameMode"`
	Fallback *Mode   `json:"fallbackMode"`
	Watch    Watch   `json:"watch"`
}

// The wire types exist only so "absent" and "present but zero" stay distinguishable
// while decoding. Every numeric field is unsigned, which is what makes -1 and 1.5
// natural decode errors instead of silent truncation.
type (
	wireFile struct {
		Version  *int         `json:"version"`
		Monitor  *wireMonitor `json:"monitor"`
		GameMode *wireMode    `json:"gameMode"`
		Fallback *wireMode    `json:"fallbackMode"`
		Watch    *wireWatch   `json:"watch"`
	}

	wireMonitor struct {
		InstancePath   string `json:"instancePath"`
		HardwareID     string `json:"hardwareId"`
		ModelWasUnique bool   `json:"modelWasUnique"`
		Label          string `json:"label"`
	}

	wireMode struct {
		Width        *uint32 `json:"width"`
		Height       *uint32 `json:"height"`
		RefreshHz    *uint32 `json:"refreshHz"`
		BitsPerPixel *uint32 `json:"bitsPerPixel"`
	}

	wireWatch struct {
		ProcessName         *string `json:"processName"`
		RestoreDelaySeconds *uint32 `json:"restoreDelaySeconds"`
	}
)

// Parse turns the bytes of a configuration file into a File, or refuses them.
//
// It reads the version on its own first. A file from a later build may legitimately
// have a body this build cannot decode, and reporting that as malformed would send
// the user off to repair a file that is not broken.
//
// Unknown keys are ignored. JSON has no comments, so a key the tool does not know is
// how a user leaves themselves a note, and the schema being closed within a version
// means the next save drops it — which is safe, because the one path that must not
// lose unknown keys (a file from a later build) never writes at all.
func Parse(data []byte) (File, error) {
	version, err := parseVersion(data)
	if err != nil {
		return File{}, err
	}
	if version > Version {
		return File{}, fmt.Errorf("%w：檔案是 version %d，這個版本最高只支援 version %d，不會修改它",
			ErrNewerVersion, version, Version)
	}
	if version < 1 {
		return File{}, fmt.Errorf("%w：version 必須是大於 0 的整數，讀到的是 %d", ErrMalformed, version)
	}

	var wire wireFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&wire); err != nil {
		return File{}, decodeError(data, err)
	}
	if err := expectEndOfDocument(data, decoder); err != nil {
		return File{}, err
	}

	file, err := wire.resolve(version)
	if err != nil {
		return File{}, err
	}
	if err := validate(file); err != nil {
		return File{}, err
	}
	return file, nil
}

// Marshal renders a File in the documented format: UTF-8 with no BOM, LF endings,
// two-space indentation, version first. HTML escaping is off because this is a file
// people open in Notepad, and a device interface path full of \u003c is not readable.
func Marshal(file File) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		return nil, fmt.Errorf("產生設定檔內容失敗：%w", err)
	}
	return buffer.Bytes(), nil
}

// Profile converts an understood configuration into the domain profile the session
// runs on.
//
// One field does not survive the trip and it is deliberate: domain.Profile.Name is
// not part of the configuration model (the schema has no name key — the monitor's
// Label is the human-facing string), so a loaded profile carries an empty Name.
//
// An absent fallbackMode stays absent: nil here becomes nil there, and the session
// derives the mode from what the monitor reports instead. The pointer that comes out
// is the profile's own, never one into this document, because a profile is passed
// around by value and a shared pointer would make one holder's edit another's.
func (f File) Profile() domain.Profile {
	profile := domain.Profile{
		Monitor: domain.MonitorIdentity{
			InstancePath:   f.Monitor.InstancePath,
			HardwareID:     f.Monitor.HardwareID,
			ModelWasUnique: f.Monitor.ModelWasUnique,
			Label:          f.Monitor.Label,
		},
		GameMode:     domainMode(f.GameMode),
		ProcessName:  f.Watch.ProcessName,
		RestoreDelay: time.Duration(f.Watch.RestoreDelaySeconds) * time.Second,
	}
	if f.Fallback != nil {
		fallback := domainMode(*f.Fallback)
		profile.FallbackMode = &fallback
	}
	return profile
}

// FromProfile is the reverse of Profile and the only place the current Version is
// stamped onto a document.
//
// The schema's unit is whole seconds, so a sub-second RestoreDelay is truncated. The
// settings dialog offers whole seconds, and anything the truncation turns into an
// unacceptable value is caught by validate before Save writes a byte.
//
// A profile with no fallback mode writes fallbackMode: null rather than a mode of the
// tool's choosing, which is what makes "derive it from the monitor" survive a save.
func FromProfile(profile domain.Profile) File {
	file := File{
		Version: Version,
		Monitor: Monitor{
			InstancePath:   profile.Monitor.InstancePath,
			HardwareID:     profile.Monitor.HardwareID,
			ModelWasUnique: profile.Monitor.ModelWasUnique,
			Label:          profile.Monitor.Label,
		},
		GameMode: fileMode(profile.GameMode),
		Watch: Watch{
			ProcessName:         profile.ProcessName,
			RestoreDelaySeconds: uint32(profile.RestoreDelay / time.Second),
		},
	}
	if profile.FallbackMode != nil {
		fallback := fileMode(*profile.FallbackMode)
		file.Fallback = &fallback
	}
	return file
}

// normalized returns a copy with the shapes Parse would have produced, so a File
// built in memory and a File read from disk are held to exactly the same rules.
func (f File) normalized() File {
	next := f
	next.Watch.ProcessName = strings.TrimSpace(f.Watch.ProcessName)
	if f.Fallback != nil {
		fallback := *f.Fallback
		next.Fallback = &fallback
	}
	return next
}

func domainMode(mode Mode) domain.Mode {
	return domain.Mode{
		Width:        mode.Width,
		Height:       mode.Height,
		RefreshHz:    mode.RefreshHz,
		BitsPerPixel: mode.BitsPerPixel,
	}
}

func fileMode(mode domain.Mode) Mode {
	return Mode{
		Width:        mode.Width,
		Height:       mode.Height,
		RefreshHz:    mode.RefreshHz,
		BitsPerPixel: mode.BitsPerPixel,
	}
}

func parseVersion(data []byte) (int, error) {
	var probe struct {
		Version *int `json:"version"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&probe); err != nil {
		return 0, decodeError(data, err)
	}
	if probe.Version == nil {
		return 0, fmt.Errorf("%w：缺少 version，這是檔案損毀而不是舊版設定檔", ErrMalformed)
	}
	return *probe.Version, nil
}

// resolve applies the presence rules — what is required, what defaults to what — and
// produces a File whose every field is a settled value.
func (w wireFile) resolve(version int) (File, error) {
	if w.Monitor == nil {
		return File{}, missingField("monitor")
	}
	if w.GameMode == nil {
		return File{}, missingField("gameMode")
	}
	gameMode, err := resolveMode("gameMode", *w.GameMode)
	if err != nil {
		return File{}, err
	}

	file := File{
		Version: version,
		Monitor: Monitor{
			InstancePath:   w.Monitor.InstancePath,
			HardwareID:     w.Monitor.HardwareID,
			ModelWasUnique: w.Monitor.ModelWasUnique,
			Label:          w.Monitor.Label,
		},
		GameMode: gameMode,
		Watch:    Watch{RestoreDelaySeconds: defaultRestoreDelaySeconds},
	}

	if w.Fallback != nil {
		fallback, err := resolveMode("fallbackMode", *w.Fallback)
		if err != nil {
			return File{}, err
		}
		file.Fallback = &fallback
	}

	if w.Watch != nil {
		if w.Watch.ProcessName != nil {
			file.Watch.ProcessName = strings.TrimSpace(*w.Watch.ProcessName)
		}
		if w.Watch.RestoreDelaySeconds != nil {
			file.Watch.RestoreDelaySeconds = *w.Watch.RestoreDelaySeconds
		}
	}
	return file, nil
}

func resolveMode(field string, w wireMode) (Mode, error) {
	if w.Width == nil {
		return Mode{}, missingField(field + ".width")
	}
	if w.Height == nil {
		return Mode{}, missingField(field + ".height")
	}
	if w.RefreshHz == nil {
		return Mode{}, missingField(field + ".refreshHz")
	}
	mode := Mode{
		Width:        *w.Width,
		Height:       *w.Height,
		RefreshHz:    *w.RefreshHz,
		BitsPerPixel: defaultBitsPerPixel,
	}
	if w.BitsPerPixel != nil {
		mode.BitsPerPixel = *w.BitsPerPixel
	}
	return mode, nil
}

// validate holds a settled File to the spec's rejection table. It runs on what Parse
// read and on what Save is about to write, so the tool can never persist a document
// it would refuse to load.
func validate(file File) error {
	if err := validateMonitor(file.Monitor); err != nil {
		return err
	}
	if err := validateMode("gameMode", file.GameMode); err != nil {
		return err
	}
	if file.Fallback != nil {
		if err := validateMode("fallbackMode", *file.Fallback); err != nil {
			return err
		}
	}
	if err := validateProcessName(file.Watch.ProcessName); err != nil {
		return err
	}
	if file.Watch.RestoreDelaySeconds > maxRestoreDelaySeconds {
		return fmt.Errorf("%w：watch.restoreDelaySeconds 是 %d，必須介於 0 與 %d 之間",
			ErrOutOfRange, file.Watch.RestoreDelaySeconds, maxRestoreDelaySeconds)
	}
	return nil
}

// validateMonitor refuses an identity that could never match anything. Both keys
// blank is not a monitor the tool might fail to find later — it is a monitor nobody
// ever picked, and saying so now is better than an unexplained 找不到顯示器.
func validateMonitor(monitor Monitor) error {
	if strings.TrimSpace(monitor.InstancePath) == "" && strings.TrimSpace(monitor.HardwareID) == "" {
		return fmt.Errorf("%w：monitor.instancePath 與 monitor.hardwareId 至少要有一個", ErrMissingField)
	}
	return nil
}

// validateMode bounds a mode by domain.MaxDimension — the same number the
// arrangement planner in internal/display refuses to plan past, which is why it
// lives in domain rather than in either package.
func validateMode(field string, mode Mode) error {
	if mode.Width == 0 || mode.Width > domain.MaxDimension {
		return fmt.Errorf("%w：%s.width 是 %d，必須介於 1 與 %d 之間",
			ErrOutOfRange, field, mode.Width, domain.MaxDimension)
	}
	if mode.Height == 0 || mode.Height > domain.MaxDimension {
		return fmt.Errorf("%w：%s.height 是 %d，必須介於 1 與 %d 之間",
			ErrOutOfRange, field, mode.Height, domain.MaxDimension)
	}
	if mode.RefreshHz < minRefreshHz || mode.RefreshHz > maxRefreshHz {
		return fmt.Errorf("%w：%s.refreshHz 是 %d，必須介於 %d 與 %d 之間（0 與 1 在 DEVMODE 中代表硬體預設，不是選擇）",
			ErrOutOfRange, field, mode.RefreshHz, minRefreshHz, maxRefreshHz)
	}
	if mode.BitsPerPixel != defaultBitsPerPixel {
		return fmt.Errorf("%w：%s.bitsPerPixel 是 %d，這個工具只套用 %d bpp",
			ErrOutOfRange, field, mode.BitsPerPixel, defaultBitsPerPixel)
	}
	return nil
}

// forbiddenProcessNameRunes are the characters Windows cannot put in a file name,
// plus the separators that would make the value path-shaped.
const forbiddenProcessNameRunes = `\/:<>"?*|`

// validateProcessName refuses a value that could never fire. Toolhelp reports bare
// image names and process.matchesExecutable compares for equality, so a path — or
// anything else that is not a plain file name — would look configured and then never
// match. An empty name is not a failure: it means "watch nothing, manual only".
func validateProcessName(name string) error {
	if name == "" {
		return nil
	}
	if count := utf8.RuneCountInString(name); count > maxProcessNameLength {
		return fmt.Errorf("%w：watch.processName 長度 %d 超過 %d 個字元",
			ErrOutOfRange, count, maxProcessNameLength)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w：watch.processName 是 %q，那是資料夾而不是執行檔名稱", ErrOutOfRange, name)
	}
	if index := strings.IndexAny(name, forbiddenProcessNameRunes); index >= 0 {
		return fmt.Errorf("%w：watch.processName %q 含有 %q；Toolhelp 回報的是純執行檔名稱，帶路徑或特殊字元的值永遠不會命中",
			ErrOutOfRange, name, name[index:index+1])
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w：watch.processName 含有控制字元 U+%04X", ErrOutOfRange, r)
		}
	}
	return nil
}

func missingField(field string) error {
	return fmt.Errorf("%w：%s", ErrMissingField, field)
}

// decodeError turns an encoding/json failure into something that names a place in
// the user's file. json reports a byte offset; a person reading their own JSON in an
// editor needs a line and a column.
func decodeError(data []byte, err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		line, column := lineColumn(data, syntax.Offset)
		return fmt.Errorf("%w：第 %d 行第 %d 欄：%v", ErrMalformed, line, column, syntax)
	}
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) {
		line, column := lineColumn(data, mismatch.Offset)
		field := mismatch.Field
		if field == "" {
			field = mismatch.Struct
		}
		return fmt.Errorf("%w：第 %d 行第 %d 欄：欄位 %s 的值是 %s，這裡要的是%s",
			ErrMalformed, line, column, field, mismatch.Value, wantedTypeName(mismatch.Type))
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		line, column := afterLastByteLineColumn(data)
		return fmt.Errorf("%w：第 %d 行第 %d 欄：%v", ErrMalformed, line, column, err)
	}
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%w：檔案是空的", ErrMalformed)
	}
	return fmt.Errorf("%w：%v", ErrMalformed, err)
}

// wantedTypeName describes, in the user's terms, what the field they got wrong is
// supposed to hold. Naming the field is not enough on its own: telling someone their
// boolean is "not an acceptable integer" sends them to fix the wrong thing.
func wantedTypeName(want reflect.Type) string {
	if want == nil {
		return "其他型別的值"
	}
	for want.Kind() == reflect.Pointer {
		want = want.Elem()
	}
	switch want.Kind() {
	case reflect.Bool:
		return "布林值（true 或 false）"
	case reflect.String:
		return "字串"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "整數"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "非負整數"
	case reflect.Float32, reflect.Float64:
		return "數字"
	case reflect.Struct, reflect.Map:
		return "物件"
	case reflect.Slice, reflect.Array:
		return "陣列"
	default:
		return want.String()
	}
}

// expectEndOfDocument refuses anything after the top-level object. A second document
// in the file means the tool and the user disagree about which one is the settings.
func expectEndOfDocument(data []byte, decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		line, column := lineColumn(data, decoder.InputOffset()+1)
		return fmt.Errorf("%w：第 %d 行第 %d 欄之後還有多餘的內容", ErrMalformed, line, column)
	}
	return nil
}

// lineColumn converts a json offset — the number of bytes read when the error was
// hit, so the offending byte is at offset-1 — into a 1-based line and column.
func lineColumn(data []byte, offset int64) (line, column int) {
	if len(data) == 0 {
		return 1, 1
	}
	if offset < 1 {
		offset = 1
	}
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	index := int(offset) - 1

	line = 1
	lineStart := 0
	for i := 0; i < index; i++ {
		if data[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	return line, index - lineStart + 1
}

// afterLastByteLineColumn names the character position a truncated document still
// needs. It deliberately does not change lineColumn's json-offset behaviour: a JSON
// syntax offset identifies an existing byte, while io.ErrUnexpectedEOF identifies the
// next missing byte after data's final byte.
func afterLastByteLineColumn(data []byte) (line, column int) {
	line, column = 1, 1
	for _, byteValue := range data {
		if byteValue == '\n' {
			line, column = line+1, 1
			continue
		}
		column++
	}
	return line, column
}
