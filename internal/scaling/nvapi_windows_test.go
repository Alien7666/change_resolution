package scaling

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

// The struct sizes below are the load-bearing facts of this whole package. NVAPI
// derives the version word a caller must stamp from sizeof(struct), so a Go layout
// that disagrees with the driver's by a single byte does not fail loudly at the
// boundary: it stamps a version the driver rejects (-9) at best, and at worst it
// makes every field offset in a payload handed to NvAPI_DISP_SetDisplayConfig point
// at the wrong bytes. No fake can catch that and no GPU is needed to catch it, which
// is why this test exists and why it asserts the measured numbers literally rather
// than recomputing them from the same expressions the code under test uses.
func TestNativeStructSizesMatchTheMeasuredX64Layout(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skipf("NVAPI struct layout is asserted for amd64; this is %s", runtime.GOARCH)
	}

	tests := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "NV_TIMINGEXT", got: unsafe.Sizeof(timingExt{}), want: 64},
		{name: "NV_TIMING", got: unsafe.Sizeof(timing{}), want: 96},
		{name: "NV_DISPLAYCONFIG_PATH_ADVANCED_TARGET_INFO_V1", got: unsafe.Sizeof(advTargetInfo{}), want: 128},
		{name: "NV_DISPLAYCONFIG_PATH_TARGET_INFO_V2", got: unsafe.Sizeof(targetInfo{}), want: 24},
		{name: "NV_DISPLAYCONFIG_SOURCE_MODE_INFO_V1", got: unsafe.Sizeof(sourceMode{}), want: 32},
		{name: "NV_DISPLAYCONFIG_PATH_INFO_V2", got: unsafe.Sizeof(pathInfo{}), want: 48},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("unsafe.Sizeof(%s) = %d, want %d", tt.name, tt.got, tt.want)
			}
		})
	}
}

// The version words are what actually travels to the driver, so assert the stamps
// themselves and not only the sizes they are built from. 0x00020030 and 0x00010080
// were read back from a working NvAPI_DISP_GetDisplayConfig on driver 32.0.16.1074;
// a deliberately wrong version is rejected with NVAPI_INCOMPATIBLE_STRUCT_VERSION,
// which is the negative control in integration_windows_test.go.
func TestStructVersionStampsMatchTheDriverABI(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skipf("NVAPI struct versions are asserted for amd64; this is %s", runtime.GOARCH)
	}

	if got := pathInfoVersion(); got != 0x00020030 {
		t.Fatalf("NV_DISPLAYCONFIG_PATH_INFO_VER2 = %#08x, want 0x00020030", got)
	}
	if got := advTargetInfoVersion(); got != 0x00010080 {
		t.Fatalf("NV_DISPLAYCONFIG_PATH_ADVANCED_TARGET_INFO_VER1 = %#08x, want 0x00010080", got)
	}
}

// makeVersion is the sizeof|version<<16 rule from nvapi.h. It is asserted separately
// because both stamps above go through it, so a bug in it would move both of them
// together and could otherwise be hidden by a compensating struct-size change.
func TestMakeVersionPacksTheSizeInTheLowWordAndTheVersionInTheHigh(t *testing.T) {
	tests := []struct {
		size    uintptr
		version uint32
		want    uint32
	}{
		{size: 48, version: 2, want: 0x00020030},
		{size: 128, version: 1, want: 0x00010080},
		{size: 40, version: 2, want: 0x00020028},
		{size: 48, version: 9, want: 0x00090030},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d_v%d", tt.size, tt.version), func(t *testing.T) {
			if got := makeVersion(tt.size, tt.version); got != tt.want {
				t.Fatalf("makeVersion(%d, %d) = %#08x, want %#08x", tt.size, tt.version, got, tt.want)
			}
		})
	}
}

// The scaling field is the only uint32 the decision layer is allowed to write, and
// it must be the exact word NvAPI_DISP_SetDisplayConfig will read. Asserting its
// offset inside NV_DISPLAYCONFIG_PATH_ADVANCED_TARGET_INFO separates "the struct is
// the right size" from "the writable field is in the right place": a transposition of
// two same-width neighbours keeps the size correct and would otherwise be invisible
// until it silently changed a refresh rate on a live desktop.
func TestScalingIsTheThirdWordOfTheAdvancedTargetInfo(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skipf("NVAPI field offsets are asserted for amd64; this is %s", runtime.GOARCH)
	}

	offsets := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "version", got: unsafe.Offsetof(advTargetInfo{}.Version), want: 0},
		{name: "rotation", got: unsafe.Offsetof(advTargetInfo{}.Rotation), want: 4},
		{name: "scaling", got: unsafe.Offsetof(advTargetInfo{}.Scaling), want: 8},
		{name: "refreshRate1K", got: unsafe.Offsetof(advTargetInfo{}.RefreshRate1K), want: 12},
		{name: "timing", got: unsafe.Offsetof(advTargetInfo{}.Timing), want: 32},
	}

	for _, tt := range offsets {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("offsetof(advTargetInfo.%s) = %d, want %d", tt.name, tt.got, tt.want)
			}
		})
	}
}

// A payload the decision layer cannot see every byte of is a payload its divergence
// check cannot defend. Every native field must therefore surface as a payloadField,
// and the one field the layer may change must be reachable as a live *uint32 that
// aliases the bytes the driver will read.
func TestNativeConfigPublishesALivePayloadForEveryTarget(t *testing.T) {
	native := &nativeConfig{
		count:    1,
		paths:    make([]pathInfo, 1),
		targets:  [][]targetInfo{make([]targetInfo, 1)},
		details:  [][]advTargetInfo{make([]advTargetInfo, 1)},
		srcModes: make([]sourceMode, 1),
	}
	native.paths[0].Version = pathInfoVersion()
	native.paths[0].TargetInfoCount = 1
	native.details[0][0].Version = advTargetInfoVersion()
	native.details[0][0].Scaling = 6
	native.targets[0][0].DisplayID = 0x80061086

	cfg := native.publish()

	if len(cfg.targets) != 1 {
		t.Fatalf("published %d writable targets, want 1", len(cfg.targets))
	}
	if cfg.targets[0].displayID != 0x80061086 {
		t.Fatalf("published displayId %#x", cfg.targets[0].displayID)
	}
	if cfg.targets[0].scaling != &native.details[0][0].Scaling {
		t.Fatal("the writable uint32 does not alias the bytes the driver will read")
	}
	if cfg.native != native {
		t.Fatal("the published config does not retain the backing slices")
	}

	// Exactly one rendered line may move when the scaling word moves, and it must be
	// the line the decision layer names. This is the same guard requireOnlyScalingChanged
	// applies, asserted here against the real field set rather than a fake one.
	before := renderConfig(cfg)
	native.details[0][0].Scaling = 2
	after := renderConfig(cfg)
	if err := requireOnlyScalingChanged(before, after, cfg.targets[0].scalingField); err != nil {
		t.Fatalf("changing the scaling word did not render as exactly one changed line: %v", err)
	}

	names := make(map[string]int, len(cfg.fields))
	for _, field := range cfg.fields {
		names[field.name]++
	}
	for name, count := range names {
		if count > 1 {
			t.Fatalf("payload field %q rendered %d times; names must be unique", name, count)
		}
	}
	for _, want := range []string{
		"paths[0].version",
		"paths[0].sourceId",
		"paths[0].targetInfoCount",
		"paths[0].targetInfo",
		"paths[0].sourceModeInfo",
		"paths[0].flags",
		"paths[0].osAdapterId",
		"paths[0].sourceMode.resolution.width",
		"paths[0].sourceMode.posX",
		"paths[0].targets[0].displayId",
		"paths[0].targets[0].details",
		"paths[0].targets[0].targetId",
		"paths[0].targets[0].details.rotation",
		"paths[0].targets[0].details.scaling",
		"paths[0].targets[0].details.timing.pclk",
		"paths[0].targets[0].details.timing.etc.name",
	} {
		if names[want] == 0 {
			t.Fatalf("payload does not render %q", want)
		}
	}
}

// Pointer-valued fields must never render their numeric value: an address differs
// between two reads of an unchanged configuration and would make the divergence
// check fire on noise instead of on a real difference.
func TestPointerFieldsRenderOnlyAsNilOrNonNil(t *testing.T) {
	native := &nativeConfig{
		count:    1,
		paths:    make([]pathInfo, 1),
		targets:  [][]targetInfo{make([]targetInfo, 1)},
		details:  [][]advTargetInfo{make([]advTargetInfo, 1)},
		srcModes: make([]sourceMode, 1),
	}
	native.paths[0].TargetInfoCount = 1
	native.paths[0].TargetInfo = uintptr(unsafe.Pointer(&native.targets[0][0]))
	native.paths[0].SourceModeInfo = uintptr(unsafe.Pointer(&native.srcModes[0]))
	native.targets[0][0].Details = uintptr(unsafe.Pointer(&native.details[0][0]))

	for _, line := range renderConfig(native.publish()) {
		name, value, _ := strings.Cut(line, "=")
		switch name {
		case "paths[0].targetInfo", "paths[0].sourceModeInfo", "paths[0].targets[0].details":
			if value != "non-nil" {
				t.Fatalf("%s rendered as %q, want \"non-nil\"", name, value)
			}
		case "paths[0].osAdapterId":
			if value != "nil" {
				t.Fatalf("%s rendered as %q, want \"nil\"", name, value)
			}
		}
	}
	runtime.KeepAlive(native)
}

func boundNativeConfig() *nativeConfig {
	native := &nativeConfig{
		count:    1,
		paths:    make([]pathInfo, 1),
		targets:  [][]targetInfo{make([]targetInfo, 1)},
		details:  [][]advTargetInfo{make([]advTargetInfo, 1)},
		srcModes: make([]sourceMode, 1),
	}
	native.paths[0].TargetInfoCount = 1
	native.paths[0].TargetInfo = uintptr(unsafe.Pointer(&native.targets[0][0]))
	native.paths[0].SourceModeInfo = uintptr(unsafe.Pointer(&native.srcModes[0]))
	native.targets[0][0].Details = uintptr(unsafe.Pointer(&native.details[0][0]))
	return native
}

func TestNativeBindingValidationAcceptsOnlyOwnedPointersAndExactCounts(t *testing.T) {
	var nilNative *nativeConfig
	if err := nilNative.validateBindings(); err == nil {
		t.Fatal("nil native config passed binding validation")
	}
	if err := (&nativeConfig{}).validateBindings(); err != nil {
		t.Fatalf("empty config: %v", err)
	}
	if err := boundNativeConfig().validateBindings(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*nativeConfig)
	}{
		{name: "path count", mutate: func(n *nativeConfig) { n.count = 2 }},
		{name: "target outer length", mutate: func(n *nativeConfig) { n.targets = nil }},
		{name: "details outer length", mutate: func(n *nativeConfig) { n.details = nil }},
		{name: "source mode length", mutate: func(n *nativeConfig) { n.srcModes = nil }},
		{name: "target count", mutate: func(n *nativeConfig) { n.paths[0].TargetInfoCount = 2 }},
		{name: "target pointer", mutate: func(n *nativeConfig) { n.paths[0].TargetInfo++ }},
		{name: "source pointer", mutate: func(n *nativeConfig) { n.paths[0].SourceModeInfo++ }},
		{name: "details length", mutate: func(n *nativeConfig) { n.details[0] = nil }},
		{name: "details pointer", mutate: func(n *nativeConfig) { n.targets[0][0].Details++ }},
		{name: "unowned OS adapter pointer", mutate: func(n *nativeConfig) { n.paths[0].OSAdapterID = 1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			native := boundNativeConfig()
			tt.mutate(native)
			if err := native.validateBindings(); err == nil {
				t.Fatal("validateBindings accepted a mutated binding")
			}
		})
	}
}

func TestEveryWriteRevalidatesBindingsMutatedAfterValidation(t *testing.T) {
	native := boundNativeConfig()
	cfg := native.publish()
	if _, err := nativeForWrite(cfg); err != nil {
		t.Fatalf("initial payload: %v", err)
	}

	// Models the validate-only driver call changing an in/out count before the
	// controller asks for the formal flags=0 set.
	native.paths[0].TargetInfoCount = 2
	if _, err := nativeForWrite(cfg); err == nil {
		t.Fatal("formal write accepted bindings changed after validation")
	}
}

func TestNativeForWriteRejectsNilAndEmptyPayloads(t *testing.T) {
	var nilNative *nativeConfig
	for _, cfg := range []*config{nil, {}, {native: nilNative}, {native: &nativeConfig{}}, {native: "not native"}} {
		if _, err := nativeForWrite(cfg); err == nil {
			t.Fatalf("nativeForWrite(%#v) succeeded", cfg)
		}
	}
}

// NvAPI_DISP_GetDisplayIdByDisplayName is the only narrow-string boundary in this
// repository. \\.\DISPLAYn is pure ASCII, where UTF-8 and every Windows ANSI code
// page agree; a name that is not must be refused rather than silently transcoded into
// a different device.
func TestOnlyAnASCIIDeviceNameCrossesTheANSIBoundary(t *testing.T) {
	tests := []struct {
		name    string
		device  string
		wantErr bool
	}{
		{name: "display1", device: `\\.\DISPLAY1`},
		{name: "display12", device: `\\.\DISPLAY12`},
		{name: "empty", device: "", wantErr: true},
		{name: "non-ascii", device: "\\\\.\\顯示器1", wantErr: true},
		{name: "embedded NUL", device: "\\\\.\\DISPLAY\x001", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := ansiDeviceName(tt.device)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ansiDeviceName(%q) accepted a name it must refuse", tt.device)
				}
				return
			}
			if err != nil {
				t.Fatalf("ansiDeviceName(%q) = %v", tt.device, err)
			}
			if got := len(encoded); got != len(tt.device)+1 {
				t.Fatalf("encoded %d bytes for %q, want %d including the NUL", got, tt.device, len(tt.device)+1)
			}
			if encoded[len(encoded)-1] != 0 {
				t.Fatalf("encoded %q is not NUL-terminated", tt.device)
			}
			if string(encoded[:len(encoded)-1]) != tt.device {
				t.Fatalf("encoded %q as %q", tt.device, encoded[:len(encoded)-1])
			}
		})
	}
}

// A status the tool reports has to name the NVAPI symbol as well as carry the driver's
// own text; a bare number sends the user to a search engine.
func TestStatusNamesTheNVAPISymbol(t *testing.T) {
	tests := []struct {
		value status
		want  string
	}{
		{value: statusOK, want: "NVAPI_OK"},
		{value: statusIncompatibleStructVersion, want: "NVAPI_INCOMPATIBLE_STRUCT_VERSION"},
		{value: statusInvalidArgument, want: "NVAPI_INVALID_ARGUMENT"},
		{value: -12345, want: "NVAPI_STATUS(-12345)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := statusName(tt.value); got != tt.want {
				t.Fatalf("statusName(%d) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// The opt-in hardware gate authorises reads and the wrong-version negative control,
// and nothing else. That promise is only worth what it can be checked against, so
// check it: no automated test in this package may be able to reach
// NvAPI_DISP_SetDisplayConfig on a real driver.
func TestNoHardwareTestCanReachTheDisplayConfigWrite(t *testing.T) {
	source, err := os.ReadFile("integration_windows_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{".Apply(", "writeConfig(", "a.setDisplayConfig", "idSetDisplayConfig"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("integration_windows_test.go mentions %q; the NVAPI hardware gate must never be able to write", forbidden)
		}
	}

	// Two occurrences and no more: the constant's declaration and the single place it
	// is resolved into an entry point. There is no second route to the write.
	binding, err := os.ReadFile("nvapi_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(binding), "idSetDisplayConfig"); got != 2 {
		t.Fatalf("idSetDisplayConfig appears %d times in the binding, want the declaration and one resolution", got)
	}
}
