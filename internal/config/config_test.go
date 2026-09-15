package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Alien7666/change_resolution/internal/domain"
)

// specSample is the document from the design spec's 格式 section, pasted verbatim.
// If the schema ever drifts from the spec, this test is the thing that notices.
const specSample = `{
  "version": 1,
  "monitor": {
    "instancePath": "\\\\?\\DISPLAY#XMI27B2#5&2b9d4d4&0&UID4357#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}",
    "hardwareId": "MONITOR\\XMI27B2",
    "modelWasUnique": true,
    "label": "Mi Monitor 27"
  },
  "gameMode": { "width": 1920, "height": 1440, "refreshHz": 180, "bitsPerPixel": 32 },
  "fallbackMode": null,
  "watch": {
    "processName": "VALORANT-Win64-Shipping.exe",
    "restoreDelaySeconds": 3
  }
}
`

// parts renders one document per table row. Every row starts from a document that
// parses and changes exactly one thing, so a rejection can only have been caused by
// the change the row's name describes.
type parts struct {
	version  string
	monitor  string
	gameMode string
	fallback string
	watch    string
}

func validParts() parts {
	return parts{
		version:  `1`,
		monitor:  `{"instancePath": "\\\\?\\DISPLAY#XMI27B2#5&2b9d4d4&0&UID4357#{guid}", "hardwareId": "MONITOR\\XMI27B2", "modelWasUnique": true, "label": "Mi Monitor 27"}`,
		gameMode: `{"width": 1920, "height": 1440, "refreshHz": 180, "bitsPerPixel": 32}`,
		fallback: `null`,
		watch:    `{"processName": "VALORANT-Win64-Shipping.exe", "restoreDelaySeconds": 3}`,
	}
}

func (p parts) render() []byte {
	return []byte(fmt.Sprintf("{\n  \"version\": %s,\n  \"monitor\": %s,\n  \"gameMode\": %s,\n  \"fallbackMode\": %s,\n  \"watch\": %s\n}\n",
		p.version, p.monitor, p.gameMode, p.fallback, p.watch))
}

func TestParseAcceptsTheSpecSampleAndConvertsItToAProfile(t *testing.T) {
	file, err := Parse([]byte(specSample))
	if err != nil {
		t.Fatalf("Parse(specSample) = %v, want nil", err)
	}
	if file.Version != Version {
		t.Errorf("Version = %d, want %d", file.Version, Version)
	}
	if file.Fallback != nil {
		t.Errorf("Fallback = %+v, want nil for an explicit null", *file.Fallback)
	}

	want := domain.Profile{
		Monitor: domain.MonitorIdentity{
			InstancePath:   `\\?\DISPLAY#XMI27B2#5&2b9d4d4&0&UID4357#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`,
			HardwareID:     `MONITOR\XMI27B2`,
			ModelWasUnique: true,
			Label:          "Mi Monitor 27",
		},
		GameMode:     domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		ProcessName:  "VALORANT-Win64-Shipping.exe",
		RestoreDelay: 3 * time.Second,
	}
	if got := file.Profile(); !reflect.DeepEqual(got, want) {
		t.Errorf("Profile() = %+v, want %+v", got, want)
	}
}

// JSON has no comments, so the format's whole mitigation for that is that a key the
// tool does not know about is skipped rather than treated as corruption.
func TestParseIgnoresUnknownKeys(t *testing.T) {
	data := []byte(`{
  "_comment": "changed 2026-09-14 after moving the cable",
  "version": 1,
  "monitor": {"hardwareId": "MONITOR\\XMI27B2", "modelWasUnique": true, "_note": 7},
  "gameMode": {"width": 1920, "height": 1440, "refreshHz": 180},
  "watch": {"processName": "VALORANT-Win64-Shipping.exe", "restoreDelaySeconds": 3},
  "futureKey": {"nested": [1, 2, 3]}
}`)
	file, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse = %v, want nil (unknown keys must be ignored)", err)
	}
	if file.GameMode.Width != 1920 {
		t.Errorf("GameMode.Width = %d, want 1920", file.GameMode.Width)
	}
}

func TestParseDefaultsBitsPerPixelTo32WhenAbsent(t *testing.T) {
	p := validParts()
	p.gameMode = `{"width": 1920, "height": 1440, "refreshHz": 180}`
	file, err := Parse(p.render())
	if err != nil {
		t.Fatalf("Parse = %v, want nil", err)
	}
	if file.GameMode.BitsPerPixel != 32 {
		t.Errorf("GameMode.BitsPerPixel = %d, want 32", file.GameMode.BitsPerPixel)
	}
}

func TestParseDefaultsTheRestoreDelayWhenTheWatchSectionIsAbsent(t *testing.T) {
	data := []byte(`{
  "version": 1,
  "monitor": {"hardwareId": "MONITOR\\XMI27B2"},
  "gameMode": {"width": 1920, "height": 1440, "refreshHz": 180}
}`)
	file, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse = %v, want nil", err)
	}
	profile := file.Profile()
	if profile.RestoreDelay != 3*time.Second {
		t.Errorf("RestoreDelay = %v, want 3s", profile.RestoreDelay)
	}
	if profile.ProcessName != "" {
		t.Errorf("ProcessName = %q, want empty (watch nothing)", profile.ProcessName)
	}
}

func TestParseAcceptsTheRestoreDelayAtBothEndsOfTheRange(t *testing.T) {
	for _, seconds := range []uint32{0, 60} {
		p := validParts()
		p.watch = fmt.Sprintf(`{"processName": "a.exe", "restoreDelaySeconds": %d}`, seconds)
		file, err := Parse(p.render())
		if err != nil {
			t.Fatalf("Parse(restoreDelaySeconds=%d) = %v, want nil", seconds, err)
		}
		if got := file.Profile().RestoreDelay; got != time.Duration(seconds)*time.Second {
			t.Errorf("RestoreDelay = %v, want %ds", got, seconds)
		}
	}
}

// An out-of-range value is rejected, never clamped: clamping rewrites what the user
// meant without telling them. Asserting the error is not enough — this asserts the
// returned file is the zero value, so nothing downstream can use a repaired config.
func TestParseRejectsAnOutOfRangeRestoreDelayInsteadOfClampingIt(t *testing.T) {
	p := validParts()
	p.watch = `{"processName": "a.exe", "restoreDelaySeconds": 61}`
	file, err := Parse(p.render())
	if !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("Parse = %v, want ErrOutOfRange", err)
	}
	if file != (File{}) {
		t.Errorf("Parse returned %+v, want the zero File — a rejected config is never partly loaded", file)
	}
	if !strings.Contains(err.Error(), "restoreDelaySeconds") {
		t.Errorf("error %q does not name the field the user has to fix", err)
	}
}

// A file written by a newer version is refused before anything else is looked at:
// the body may legitimately have a shape this version cannot decode, and reporting
// "malformed" for it would send the user off to fix a file that is not broken.
func TestParseRejectsANewerVersionBeforeJudgingTheRestOfTheFile(t *testing.T) {
	data := []byte(`{
  "version": 2,
  "profiles": [{"monitor": {"hardwareId": "MONITOR\\XMI27B2"}}]
}`)
	file, err := Parse(data)
	if !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("Parse = %v, want ErrNewerVersion", err)
	}
	if file != (File{}) {
		t.Errorf("Parse returned %+v, want the zero File", file)
	}
}

func TestParseAcceptsAProcessNameWithoutAnExeSuffix(t *testing.T) {
	p := validParts()
	p.watch = `{"processName": "some-game", "restoreDelaySeconds": 3}`
	file, err := Parse(p.render())
	if err != nil {
		t.Fatalf("Parse = %v, want nil (Toolhelp reports the real image name)", err)
	}
	if got := file.Profile().ProcessName; got != "some-game" {
		t.Errorf("ProcessName = %q, want %q", got, "some-game")
	}
}

// An absent, empty or whitespace-only process name is a legitimate configuration
// meaning "watch nothing, manual only". It normalises to the empty string so the UI
// has exactly one value to test when it says 自動恢復：未設定.
func TestParseAcceptsABlankProcessNameAsWatchNothing(t *testing.T) {
	for _, watch := range []string{
		`{"restoreDelaySeconds": 3}`,
		`{"processName": "", "restoreDelaySeconds": 3}`,
		`{"processName": "   ", "restoreDelaySeconds": 3}`,
	} {
		p := validParts()
		p.watch = watch
		file, err := Parse(p.render())
		if err != nil {
			t.Fatalf("Parse(watch=%s) = %v, want nil", watch, err)
		}
		if got := file.Profile().ProcessName; got != "" {
			t.Errorf("ProcessName = %q, want empty for watch=%s", got, watch)
		}
	}
}

type rejectCase struct {
	name    string
	mutate  func(parts) parts
	raw     string // used instead of mutate when non-empty
	wantErr error
	wantMsg string
}

func TestParseRejects(t *testing.T) {
	tests := []rejectCase{
		{
			name:    "version missing",
			raw:     `{"monitor": {"hardwareId": "MONITOR\\XMI27B2"}, "gameMode": {"width": 1920, "height": 1440, "refreshHz": 180}}`,
			wantErr: ErrMalformed,
			wantMsg: "version",
		},
		{
			name:    "version is a string",
			mutate:  func(p parts) parts { p.version = `"1"`; return p },
			wantErr: ErrMalformed,
			wantMsg: "version",
		},
		{
			name:    "version is fractional",
			mutate:  func(p parts) parts { p.version = `1.5`; return p },
			wantErr: ErrMalformed,
			wantMsg: "version",
		},
		{
			name:    "version is zero",
			mutate:  func(p parts) parts { p.version = `0`; return p },
			wantErr: ErrMalformed,
			wantMsg: "version",
		},
		{
			name:    "version is newer than this build",
			mutate:  func(p parts) parts { p.version = `2`; return p },
			wantErr: ErrNewerVersion,
			wantMsg: "2",
		},
		{
			name:    "malformed json",
			raw:     "{\n  \"version\": 1,\n  \"monitor\": }\n}\n",
			wantErr: ErrMalformed,
			wantMsg: "第 3 行",
		},
		{
			name:    "trailing content after the document",
			raw:     `{"version": 1, "monitor": {"hardwareId": "X"}, "gameMode": {"width": 1, "height": 1, "refreshHz": 60}} trailing`,
			wantErr: ErrMalformed,
		},
		{
			name:    "monitor missing",
			raw:     `{"version": 1, "gameMode": {"width": 1920, "height": 1440, "refreshHz": 180}}`,
			wantErr: ErrMissingField,
			wantMsg: "monitor",
		},
		{
			name:    "monitor null",
			mutate:  func(p parts) parts { p.monitor = `null`; return p },
			wantErr: ErrMissingField,
			wantMsg: "monitor",
		},
		{
			name:    "monitor carries neither key",
			mutate:  func(p parts) parts { p.monitor = `{"label": "Mi Monitor 27"}`; return p },
			wantErr: ErrMissingField,
			wantMsg: "instancePath",
		},
		{
			name:    "gameMode missing",
			raw:     `{"version": 1, "monitor": {"hardwareId": "MONITOR\\XMI27B2"}}`,
			wantErr: ErrMissingField,
			wantMsg: "gameMode",
		},
		{
			name:    "gameMode null",
			mutate:  func(p parts) parts { p.gameMode = `null`; return p },
			wantErr: ErrMissingField,
			wantMsg: "gameMode",
		},
		{
			name:    "width missing",
			mutate:  func(p parts) parts { p.gameMode = `{"height": 1440, "refreshHz": 180}`; return p },
			wantErr: ErrMissingField,
			wantMsg: "gameMode.width",
		},
		{
			name:    "width negative",
			mutate:  func(p parts) parts { p.gameMode = `{"width": -1, "height": 1440, "refreshHz": 180}`; return p },
			wantErr: ErrMalformed,
			wantMsg: "width",
		},
		{
			name:    "width fractional",
			mutate:  func(p parts) parts { p.gameMode = `{"width": 1920.5, "height": 1440, "refreshHz": 180}`; return p },
			wantErr: ErrMalformed,
			wantMsg: "width",
		},
		{
			name:    "width zero",
			mutate:  func(p parts) parts { p.gameMode = `{"width": 0, "height": 1440, "refreshHz": 180}`; return p },
			wantErr: ErrOutOfRange,
			wantMsg: "gameMode.width",
		},
		{
			name: "width past the shared dimension bound",
			mutate: func(p parts) parts {
				p.gameMode = fmt.Sprintf(`{"width": %d, "height": 1440, "refreshHz": 180}`, domain.MaxDimension+1)
				return p
			},
			wantErr: ErrOutOfRange,
			wantMsg: "gameMode.width",
		},
		{
			name:    "height zero",
			mutate:  func(p parts) parts { p.gameMode = `{"width": 1920, "height": 0, "refreshHz": 180}`; return p },
			wantErr: ErrOutOfRange,
			wantMsg: "gameMode.height",
		},
		{
			name: "height past the shared dimension bound",
			mutate: func(p parts) parts {
				p.gameMode = fmt.Sprintf(`{"width": 1920, "height": %d, "refreshHz": 180}`, domain.MaxDimension+1)
				return p
			},
			wantErr: ErrOutOfRange,
			wantMsg: "gameMode.height",
		},
		{
			name:    "refreshHz missing",
			mutate:  func(p parts) parts { p.gameMode = `{"width": 1920, "height": 1440}`; return p },
			wantErr: ErrMissingField,
			wantMsg: "gameMode.refreshHz",
		},
		{
			// 0 and 1 mean "let the hardware decide" in DEVMODE, which is never
			// something a user picked out of an enumerated list.
			name:    "refreshHz zero",
			mutate:  func(p parts) parts { p.gameMode = `{"width": 1920, "height": 1440, "refreshHz": 0}`; return p },
			wantErr: ErrOutOfRange,
			wantMsg: "gameMode.refreshHz",
		},
		{
			name:    "refreshHz one",
			mutate:  func(p parts) parts { p.gameMode = `{"width": 1920, "height": 1440, "refreshHz": 1}`; return p },
			wantErr: ErrOutOfRange,
			wantMsg: "gameMode.refreshHz",
		},
		{
			name:    "refreshHz past 1000",
			mutate:  func(p parts) parts { p.gameMode = `{"width": 1920, "height": 1440, "refreshHz": 1001}`; return p },
			wantErr: ErrOutOfRange,
			wantMsg: "gameMode.refreshHz",
		},
		{
			name: "bitsPerPixel other than 32",
			mutate: func(p parts) parts {
				p.gameMode = `{"width": 1920, "height": 1440, "refreshHz": 180, "bitsPerPixel": 16}`
				return p
			},
			wantErr: ErrOutOfRange,
			wantMsg: "gameMode.bitsPerPixel",
		},
		{
			name: "fallbackMode is validated exactly like the game mode",
			mutate: func(p parts) parts {
				p.fallback = `{"width": 2560, "height": 1440, "refreshHz": 1}`
				return p
			},
			wantErr: ErrOutOfRange,
			wantMsg: "fallbackMode.refreshHz",
		},
		{
			name:    "restoreDelaySeconds negative",
			mutate:  func(p parts) parts { p.watch = `{"restoreDelaySeconds": -1}`; return p },
			wantErr: ErrMalformed,
			wantMsg: "restoreDelaySeconds",
		},
		{
			name:    "restoreDelaySeconds fractional",
			mutate:  func(p parts) parts { p.watch = `{"restoreDelaySeconds": 2.5}`; return p },
			wantErr: ErrMalformed,
			wantMsg: "restoreDelaySeconds",
		},
		{
			name:    "restoreDelaySeconds past 60",
			mutate:  func(p parts) parts { p.watch = `{"restoreDelaySeconds": 61}`; return p },
			wantErr: ErrOutOfRange,
			wantMsg: "restoreDelaySeconds",
		},
	}

	// A path-shaped or otherwise impossible process name rejects the whole file:
	// Toolhelp reports bare image names and matchesExecutable compares for equality,
	// so such a value would look configured and then never fire — which is worse
	// than saying so. The escaping here is JSON's, inside a Go raw string.
	for _, bad := range []string{
		`C:\\Games\\VALORANT.exe`,
		`Riot/VALORANT.exe`,
		`C:VALORANT.exe`,
		`VALO<RANT.exe`,
		`VALO>RANT.exe`,
		`VALO\"RANT.exe`,
		`VALO?RANT.exe`,
		`VALO*RANT.exe`,
		`VALO|RANT.exe`,
		`VALO\u0007RANT.exe`,
		`.`,
		`..`,
		strings.Repeat("a", 256),
	} {
		tests = append(tests, rejectCase{
			name:    "processName " + bad,
			mutate:  func(p parts) parts { p.watch = `{"processName": "` + bad + `"}`; return p },
			wantErr: ErrOutOfRange,
			wantMsg: "processName",
		})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.raw)
			if tc.raw == "" {
				data = tc.mutate(validParts()).render()
			}
			file, err := Parse(data)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Parse = %v, want %v\n%s", err, tc.wantErr, data)
			}
			if file != (File{}) {
				t.Errorf("Parse returned %+v, want the zero File — a rejected config is never partly loaded", file)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tc.wantMsg)
			}
		})
	}
}

// The user is the author of this file, so a syntax error has to say where to look.
func TestParseReportsTheLineAndColumnOfASyntaxError(t *testing.T) {
	data := []byte("{\n  \"version\": 1,\n  \"monitor\": }\n}\n")
	_, err := Parse(data)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("Parse = %v, want ErrMalformed", err)
	}
	for _, want := range []string{"第 3 行", "第 14 欄"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// A truncated document has no offending byte. Its useful location is the next byte the
// parser needed: after the last byte, including the first column of a final blank line.
func TestParseReportsTheNextMissingPositionForTruncatedJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []string
	}{
		{
			name: "after the last byte on a populated line",
			data: "{\n  \"version\": 1,",
			want: []string{"第 2 行", "第 16 欄", "unexpected EOF"},
		},
		{
			name: "first column after a trailing newline",
			data: "{\n  \"version\": 1,\n",
			want: []string{"第 3 行", "第 1 欄", "unexpected EOF"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.data))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("Parse = %v, want ErrMalformed", err)
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// Naming the field is not enough on its own. Telling someone that their boolean is
// not an acceptable integer sends them off to fix the wrong thing, so the message
// says what the field it names actually holds.
func TestParseSaysWhatTypeTheFieldItNamesActuallyWants(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(parts) parts
		wantMsg string
	}{
		{
			name:    "a boolean given a string",
			mutate:  func(p parts) parts { p.monitor = `{"hardwareId": "X", "modelWasUnique": "yes"}`; return p },
			wantMsg: "布林值",
		},
		{
			name:    "a string given a number",
			mutate:  func(p parts) parts { p.watch = `{"processName": 123}`; return p },
			wantMsg: "字串",
		},
		{
			name:    "an object given an array",
			mutate:  func(p parts) parts { p.gameMode = `[1920, 1440, 180]`; return p },
			wantMsg: "物件",
		},
		{
			name:    "a number given a string",
			mutate:  func(p parts) parts { p.gameMode = `{"width": "1920", "height": 1440, "refreshHz": 180}`; return p },
			wantMsg: "非負整數",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.mutate(validParts()).render())
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("Parse = %v, want ErrMalformed", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not say the field wants %s", err, tc.wantMsg)
			}
		})
	}
}

func TestLineColumn(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		offset     int64
		wantLine   int
		wantColumn int
	}{
		{name: "first byte", data: "abc", offset: 1, wantLine: 1, wantColumn: 1},
		{name: "third byte", data: "abc", offset: 3, wantLine: 1, wantColumn: 3},
		{name: "start of the second line", data: "a\nbc", offset: 3, wantLine: 2, wantColumn: 1},
		{name: "inside the second line", data: "a\nbc", offset: 4, wantLine: 2, wantColumn: 2},
		{name: "after a blank line", data: "a\n\nx", offset: 4, wantLine: 3, wantColumn: 1},
		{name: "offset past the end clamps", data: "a\nb", offset: 99, wantLine: 2, wantColumn: 1},
		{name: "offset before the start clamps", data: "ab", offset: 0, wantLine: 1, wantColumn: 1},
		{name: "empty input", data: "", offset: 1, wantLine: 1, wantColumn: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line, column := lineColumn([]byte(tc.data), tc.offset)
			if line != tc.wantLine || column != tc.wantColumn {
				t.Errorf("lineColumn(%q, %d) = (%d, %d), want (%d, %d)",
					tc.data, tc.offset, line, column, tc.wantLine, tc.wantColumn)
			}
		})
	}
}

// roundTripProfile is everything the v1 schema can carry. Profile.Name is absent
// from it on purpose: the spec's field table has no name key, so the configuration
// does not store one.
func roundTripProfile() domain.Profile {
	return domain.Profile{
		Monitor: domain.MonitorIdentity{
			InstancePath:   `\\?\DISPLAY#XMI27B2#5&2b9d4d4&0&UID4357#{guid}`,
			HardwareID:     `MONITOR\XMI27B2`,
			ModelWasUnique: true,
			Label:          "Mi Monitor 27",
		},
		GameMode:     domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		FallbackMode: &domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		ProcessName:  "VALORANT-Win64-Shipping.exe",
		RestoreDelay: 3 * time.Second,
	}
}

func TestFromProfileRoundTripsThroughParse(t *testing.T) {
	profile := roundTripProfile()
	data, err := Marshal(FromProfile(profile))
	if err != nil {
		t.Fatalf("Marshal = %v", err)
	}
	file, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(Marshal(FromProfile(p))) = %v, want nil\n%s", err, data)
	}
	if got := file.Profile(); !reflect.DeepEqual(got, profile) {
		t.Errorf("round trip = %+v, want %+v\n%s", got, profile, data)
	}
}

func TestFromProfileWritesANullFallbackWhenTheProfileHasNone(t *testing.T) {
	profile := roundTripProfile()
	profile.FallbackMode = nil
	file := FromProfile(profile)
	if file.Fallback != nil {
		t.Fatalf("Fallback = %+v, want nil", *file.Fallback)
	}
	data, err := Marshal(file)
	if err != nil {
		t.Fatalf("Marshal = %v", err)
	}
	if !strings.Contains(string(data), `"fallbackMode": null`) {
		t.Errorf("document does not carry a null fallbackMode:\n%s", data)
	}
}

// The format is fixed by the spec: UTF-8 without a BOM, LF, two-space indent, and
// version as the first key. A user opens this file in Notepad.
func TestMarshalWritesTheDocumentedFormat(t *testing.T) {
	data, err := Marshal(FromProfile(roundTripProfile()))
	if err != nil {
		t.Fatalf("Marshal = %v", err)
	}
	text := string(data)
	if strings.HasPrefix(text, "\ufeff") {
		t.Error("document starts with a BOM")
	}
	if strings.Contains(text, "\r") {
		t.Error("document contains CR")
	}
	if !strings.HasPrefix(text, "{\n  \"version\": 1,\n") {
		t.Errorf("version is not the first key at two-space indent:\n%s", text)
	}
	if !strings.HasSuffix(text, "}\n") {
		t.Errorf("document does not end with a single newline:\n%q", text)
	}
	// Marshalling must not HTML-escape: an instance path is full of characters
	// encoding/json would turn into \u003c-style noise in a file people edit.
	if strings.Contains(text, `\u00`) {
		t.Errorf("document contains HTML escaping:\n%s", text)
	}
}

// Ruling 3 of the continuation plan, both directions: an absent fallback is nil on
// the document side and nil on the profile side, and nothing between them quietly
// substitutes a mode of its own.
func TestAnAbsentFallbackIsNilInBothDirections(t *testing.T) {
	file, err := Parse(validParts().render())
	if err != nil {
		t.Fatalf("Parse = %v, want nil", err)
	}
	if file.Fallback != nil {
		t.Fatalf("Fallback = %+v, want nil", *file.Fallback)
	}
	if profile := file.Profile(); profile.FallbackMode != nil {
		t.Fatalf("Profile().FallbackMode = %+v, want nil", *profile.FallbackMode)
	}
	profile := roundTripProfile()
	profile.FallbackMode = nil
	if back := FromProfile(profile); back.Fallback != nil {
		t.Fatalf("FromProfile().Fallback = %+v, want nil", *back.Fallback)
	}
}

// A configured fallback survives the conversion and is not shared with the document
// it came from: the profile is handed around by value, and a pointer into the parsed
// file would make one holder's edit another holder's surprise.
func TestAConfiguredFallbackIsCopiedRatherThanShared(t *testing.T) {
	parts := validParts()
	parts.fallback = `{"width": 2560, "height": 1440, "refreshHz": 180, "bitsPerPixel": 32}`
	file, err := Parse(parts.render())
	if err != nil {
		t.Fatalf("Parse = %v, want nil", err)
	}
	want := domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	profile := file.Profile()
	if profile.FallbackMode == nil || *profile.FallbackMode != want {
		t.Fatalf("Profile().FallbackMode = %+v, want %+v", profile.FallbackMode, want)
	}
	*profile.FallbackMode = domain.Mode{Width: 640, Height: 480, RefreshHz: 60, BitsPerPixel: 32}
	if file.Fallback.Width != want.Width {
		t.Fatalf("changing the profile changed the file it was read from: %+v", *file.Fallback)
	}

	back := FromProfile(profile)
	if back.Fallback == nil || back.Fallback.Width != 640 {
		t.Fatalf("FromProfile().Fallback = %+v, want the profile's own mode", back.Fallback)
	}
	*profile.FallbackMode = want
	if back.Fallback.Width != 640 {
		t.Fatalf("changing the profile changed the file built from it: %+v", *back.Fallback)
	}
}
