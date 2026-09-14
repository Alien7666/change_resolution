package display

import (
	"errors"
	"os"
	"strings"
	"testing"
	"unsafe"
)

// The CCD structs cross an ABI boundary, so their sizes are the contract with
// wingdi.h and are taken from the SDK rather than measured from whatever Go happens
// to lay out. Each number below is the sum of the documented members:
//
//	LUID                             8   DWORD LowPart + LONG HighPart
//	DISPLAYCONFIG_RATIONAL           8   two UINT32
//	DISPLAYCONFIG_PATH_SOURCE_INFO  20   8 + 4 + 4 + 4
//	DISPLAYCONFIG_PATH_TARGET_INFO  48   8 + 4 + 4 + 4 + 4 + 4 + 8 + 4 + 4 + 4
//	DISPLAYCONFIG_PATH_INFO         72   20 + 48 + 4
//	DISPLAYCONFIG_MODE_INFO         64   4 + 4 + 8, then a 48-byte union at offset 16
//	DISPLAYCONFIG_DEVICE_INFO_HEADER 20  4 + 4 + 8 + 4
//	DISPLAYCONFIG_TARGET_DEVICE_NAME 420 20 + 4 + 4 + 2 + 2 + 4 + 128 + 256
//
// A wrong size here is not a compile error and not a test failure anywhere else: it
// is QueryDisplayConfig walking off the end of an array it was told was larger than
// it is. The opt-in integration test is what proves these numbers against the real
// API; this test is what stops them drifting silently.
func TestCCDStructsMatchTheWindowsSDK(t *testing.T) {
	for name, got := range map[string]uintptr{
		"LUID":                             unsafe.Sizeof(luid{}),
		"DISPLAYCONFIG_RATIONAL":           unsafe.Sizeof(displayConfigRational{}),
		"DISPLAYCONFIG_PATH_SOURCE_INFO":   unsafe.Sizeof(displayConfigPathSourceInfo{}),
		"DISPLAYCONFIG_PATH_TARGET_INFO":   unsafe.Sizeof(displayConfigPathTargetInfo{}),
		"DISPLAYCONFIG_PATH_INFO":          unsafe.Sizeof(displayConfigPathInfo{}),
		"DISPLAYCONFIG_MODE_INFO":          unsafe.Sizeof(displayConfigModeInfo{}),
		"DISPLAYCONFIG_DEVICE_INFO_HEADER": unsafe.Sizeof(displayConfigDeviceInfoHeader{}),
		"DISPLAYCONFIG_TARGET_DEVICE_NAME": unsafe.Sizeof(displayConfigTargetDeviceName{}),
	} {
		want := map[string]uintptr{
			"LUID": 8, "DISPLAYCONFIG_RATIONAL": 8,
			"DISPLAYCONFIG_PATH_SOURCE_INFO": 20, "DISPLAYCONFIG_PATH_TARGET_INFO": 48,
			"DISPLAYCONFIG_PATH_INFO": 72, "DISPLAYCONFIG_MODE_INFO": 64,
			"DISPLAYCONFIG_DEVICE_INFO_HEADER": 20, "DISPLAYCONFIG_TARGET_DEVICE_NAME": 420,
		}[name]
		if got != want {
			t.Errorf("sizeof(%s)=%d, want %d", name, got, want)
		}
	}
	// The union in DISPLAYCONFIG_MODE_INFO starts after the three fixed members, and
	// the request packet's name buffers have to sit where the SDK puts them or the
	// driver writes the friendly name over the device path.
	if got := unsafe.Offsetof(displayConfigModeInfo{}.Data); got != 16 {
		t.Errorf("DISPLAYCONFIG_MODE_INFO union offset=%d, want 16", got)
	}
	if got := unsafe.Offsetof(displayConfigTargetDeviceName{}.MonitorFriendlyDeviceName); got != 36 {
		t.Errorf("monitorFriendlyDeviceName offset=%d, want 36", got)
	}
	if got := unsafe.Offsetof(displayConfigTargetDeviceName{}.MonitorDevicePath); got != 164 {
		t.Errorf("monitorDevicePath offset=%d, want 164", got)
	}
}

// The flag and request values are the contract with wingdi.h. Only the reading half
// of the CCD API may appear: QDC_ONLY_ACTIVE_PATHS asks for the arrangement in use,
// and GET_TARGET_NAME asks a target what it is called. SET_TARGET_PERSISTENCE (4)
// and the whole SetDisplayConfig family are the registry-writing side of this API --
// the same mistake CDS_UPDATEREGISTRY would be -- so the source file is read back
// here and held to containing none of them.
func TestCCDUsesOnlyTheReadingHalfOfTheAPI(t *testing.T) {
	if qdcOnlyActivePaths != 0x00000002 {
		t.Errorf("QDC_ONLY_ACTIVE_PATHS=%#x", qdcOnlyActivePaths)
	}
	if displayConfigDeviceInfoGetTargetName != 2 {
		t.Errorf("DISPLAYCONFIG_DEVICE_INFO_GET_TARGET_NAME=%d", displayConfigDeviceInfoGetTargetName)
	}
	source, err := os.ReadFile("ccd_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"DisplayConfigSetDeviceInfo",
		"SetDisplayConfig",
		"SET_TARGET_PERSISTENCE",
	} {
		if strings.Contains(string(source), forbidden) {
			t.Errorf("ccd_windows.go mentions %s; this file is read-only", forbidden)
		}
	}
}

// The ladder is the whole point of the task: a monitor list where the rows read
// "Generic PnP Monitor" four times is a list in which picking a row is a guess, and
// a guess here applies a display mode to the wrong screen.
func TestChooseLabelWalksTheNameLadder(t *testing.T) {
	tests := []struct {
		name         string
		friendly     string
		deviceString string
		hardwareID   string
		want         string
	}{
		{
			name:         "the CCD name wins",
			friendly:     "Mi Monitor 27",
			deviceString: "Generic PnP Monitor",
			hardwareID:   `MONITOR\XMI27B2\0009`,
			want:         "Mi Monitor 27",
		},
		{
			name:         "an absent CCD name falls back to the monitor's own name",
			deviceString: "Acer XB271HU",
			hardwareID:   `MONITOR\ACR0D0D\0004`,
			want:         "Acer XB271HU",
		},
		{
			name:         "a blank CCD name is the same as an absent one",
			friendly:     "   ",
			deviceString: "Acer XB271HU",
			hardwareID:   `MONITOR\ACR0D0D\0004`,
			want:         "Acer XB271HU",
		},
		{
			name:       "an empty DeviceString falls through to the hardware ID",
			hardwareID: `MONITOR\XMI27B2\0009`,
			want:       `MONITOR\XMI27B2\0009`,
		},
		{
			name:         "the placeholder Windows invents is not a name",
			deviceString: "Generic PnP Monitor",
			hardwareID:   `MONITOR\XMI27B2\0009`,
			want:         `MONITOR\XMI27B2\0009`,
		},
		{
			name:         "the placeholder is recognised whatever its casing",
			deviceString: "generic pnp MONITOR",
			hardwareID:   `MONITOR\XMI27B2\0009`,
			want:         `MONITOR\XMI27B2\0009`,
		},
		{
			name:       "a CCD name that is itself the placeholder falls through too",
			friendly:   "Generic PnP Monitor",
			hardwareID: `MONITOR\XMI27B2\0009`,
			want:       `MONITOR\XMI27B2\0009`,
		},
		{
			// The last rung is not "say nothing": a placeholder still tells the user
			// there is a monitor on this row, and a blank row tells them nothing at
			// all. Falling back past the hardware ID only happens when there is no
			// hardware ID either.
			name:         "a placeholder is still better than an empty row",
			deviceString: "Generic PnP Monitor",
			want:         "Generic PnP Monitor",
		},
		{
			name: "nothing to show stays nothing rather than becoming invented text",
			want: "",
		},
		{
			name:         "surrounding whitespace never reaches the user",
			friendly:     "  Mi Monitor 27  ",
			deviceString: "Generic PnP Monitor",
			want:         "Mi Monitor 27",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chooseLabel(tt.friendly, tt.deviceString, tt.hardwareID); got != tt.want {
				t.Errorf("chooseLabel(%q, %q, %q)=%q, want %q",
					tt.friendly, tt.deviceString, tt.hardwareID, got, tt.want)
			}
		})
	}
}

// The friendly name and the hardware enumeration are two different reads of the same
// desktop, and monitorDevicePath is what joins them: it is the same device interface
// path EnumDisplayDevicesW reports under EDD_GET_DEVICE_INTERFACE_NAME. The two APIs
// do not agree on casing, so the join is case-insensitive on both sides.
func TestCCDNamerKeysFriendlyNamesByTheDeviceInterfacePath(t *testing.T) {
	api := &fakeCCD{targets: []ccdTarget{
		{id: 4357, friendly: "Mi Monitor 27", devicePath: miMonitorInterfacePath},
		{id: 4358, friendly: "Acer XB271HU", devicePath: `\\?\DISPLAY#ACR0D0D#5&2b9d4d4&0&UID4360#{GUID}`},
	}}
	namer := &ccdNamer{api: api}

	names, err := namer.friendlyNames()
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupFriendlyName(names, strings.ToUpper(miMonitorInterfacePath)); got != "Mi Monitor 27" {
		t.Errorf("uppercased lookup=%q, want %q", got, "Mi Monitor 27")
	}
	if got := lookupFriendlyName(names, `\\?\display#acr0d0d#5&2b9d4d4&0&uid4360#{guid}`); got != "Acer XB271HU" {
		t.Errorf("lowercased lookup=%q, want %q", got, "Acer XB271HU")
	}
	if got := lookupFriendlyName(names, ""); got != "" {
		t.Errorf("a monitor with no interface path matched %q", got)
	}
	if got := lookupFriendlyName(names, `\\?\DISPLAY#NOTHERE#0#{GUID}`); got != "" {
		t.Errorf("an unknown path matched %q", got)
	}
	if api.infoCalls != 2 {
		t.Errorf("DisplayConfigGetDeviceInfo calls=%d, want one per active target", api.infoCalls)
	}
}

// GetDisplayConfigBufferSizes and QueryDisplayConfig are two separate crossings, and
// the desktop may change between them -- a monitor waking up is enough. Windows
// answers that race with ERROR_INSUFFICIENT_BUFFER and expects the caller to size
// again, so a single attempt would turn an ordinary event into a lost name.
func TestCCDNamerRetriesWhenTheTopologyChangesBetweenSizingAndQuerying(t *testing.T) {
	api := &fakeCCD{
		targets:       []ccdTarget{{id: 4357, friendly: "Mi Monitor 27", devicePath: miMonitorInterfacePath}},
		queryStatuses: []int32{errorInsufficientBuffer, errorInsufficientBuffer, errorSuccess},
	}
	namer := &ccdNamer{api: api}

	names, err := namer.friendlyNames()
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupFriendlyName(names, miMonitorInterfacePath); got != "Mi Monitor 27" {
		t.Fatalf("name=%q after two resizes", got)
	}
	if api.sizeCalls != 3 {
		t.Errorf("GetDisplayConfigBufferSizes calls=%d, want one per attempt", api.sizeCalls)
	}
}

// A desktop that keeps changing under the read is not an error the user should ever
// see -- it costs them a prettier name and nothing else -- but the namer still has
// to stop rather than spin, and still has to say why to whoever is reading a log.
func TestCCDNamerGivesUpRatherThanRetryingForever(t *testing.T) {
	api := &fakeCCD{
		targets:       []ccdTarget{{id: 4357, friendly: "Mi Monitor 27", devicePath: miMonitorInterfacePath}},
		queryStatuses: []int32{errorInsufficientBuffer},
	}
	namer := &ccdNamer{api: api}

	if _, err := namer.friendlyNames(); err == nil {
		t.Fatal("a query that never succeeds must report an error to its caller")
	}
	if api.sizeCalls < 2 || api.sizeCalls > 8 {
		t.Errorf("attempts=%d, want a small bounded number", api.sizeCalls)
	}
}

func TestCCDNamerReportsTheQueryFailureItCannotRecoverFrom(t *testing.T) {
	api := &fakeCCD{sizesStatus: errorGenFailure}
	namer := &ccdNamer{api: api}

	if _, err := namer.friendlyNames(); err == nil {
		t.Fatal("GetDisplayConfigBufferSizes failing must not look like an empty desktop")
	}
	if api.queryCalls != 0 {
		t.Errorf("QueryDisplayConfig was called %d times after sizing failed", api.queryCalls)
	}
}

// One target that will not answer must not cost the other monitors their names. The
// whole feature is an enrichment, so every failure inside it is partial at worst.
func TestCCDNamerReportsTheOtherMonitorsWhenOneTargetWillNotAnswer(t *testing.T) {
	api := &fakeCCD{targets: []ccdTarget{
		{id: 4357, status: errorGenFailure, friendly: "never read", devicePath: miMonitorInterfacePath},
		{id: 4358, friendly: "Acer XB271HU", devicePath: `\\?\DISPLAY#ACR0D0D#0#{GUID}`},
	}}
	namer := &ccdNamer{api: api}

	names, err := namer.friendlyNames()
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupFriendlyName(names, miMonitorInterfacePath); got != "" {
		t.Errorf("the unreadable target produced %q", got)
	}
	if got := lookupFriendlyName(names, `\\?\DISPLAY#ACR0D0D#0#{GUID}`); got != "Acer XB271HU" {
		t.Errorf("the readable target produced %q", got)
	}
}

// A target that reports a name but no device path cannot be joined to anything, and
// a target that reports a path but no name has nothing to contribute. Neither is an
// error; both simply do not appear in the map.
func TestCCDNamerSkipsTargetsThatCannotBeJoinedOrHaveNothingToSay(t *testing.T) {
	api := &fakeCCD{targets: []ccdTarget{
		{id: 1, friendly: "Nameless Path"},
		{id: 2, devicePath: `\\?\DISPLAY#NONAME#0#{GUID}`},
	}}
	namer := &ccdNamer{api: api}

	names, err := namer.friendlyNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("names=%#v, want nothing joinable", names)
	}
}

// The CCD entry points arrived in Windows 7. Calling a proc that user32 does not
// export panics inside the syscall package, so the namer has to ask first, and a
// machine without the API is an ordinary read that reports no names.
func TestCCDNamerReportsUnavailabilityInsteadOfCallingAMissingProc(t *testing.T) {
	api := &fakeCCD{unavailable: errors.New("EntryPointNotFoundException")}
	namer := &ccdNamer{api: api}

	_, err := namer.friendlyNames()
	if err == nil {
		t.Fatal("an unavailable API must be reported, not treated as an empty desktop")
	}
	if api.sizeCalls != 0 || api.queryCalls != 0 || api.infoCalls != 0 {
		t.Errorf("a missing proc was still called: %+v", api)
	}
}

// The two failure codes below are the ones the fake hands back to exercise the
// namer's recovery paths: ERROR_GEN_FAILURE is what a driver that has gone away
// answers with, and ERROR_INVALID_PARAMETER is what Windows answers when a request
// packet names a target that is no longer in the configuration.
const (
	errorGenFailure       int32 = 31
	errorInvalidParameter int32 = 87
)

// ccdTarget is one row of the fake display configuration: the target id the path
// carries, what the driver answers when asked for that target's name, and the
// interface path that joins it to EnumDisplayDevicesW.
type ccdTarget struct {
	id         uint32
	friendly   string
	devicePath string
	status     int32
}

// fakeCCD stands in for the three user32 entry points. queryStatuses is consumed one
// value per QueryDisplayConfig call and its last value repeats, which is how a test
// models both a topology that settles after a retry and one that never does.
type fakeCCD struct {
	targets       []ccdTarget
	unavailable   error
	sizesStatus   int32
	queryStatuses []int32
	sizeCalls     int
	queryCalls    int
	infoCalls     int
}

func (f *fakeCCD) available() error { return f.unavailable }

func (f *fakeCCD) getDisplayConfigBufferSizes(flags uint32, pathCount, modeCount *uint32) (int32, error) {
	f.sizeCalls++
	if f.sizesStatus != errorSuccess {
		return f.sizesStatus, nil
	}
	*pathCount = uint32(len(f.targets))
	*modeCount = uint32(len(f.targets))
	return errorSuccess, nil
}

func (f *fakeCCD) queryDisplayConfig(
	flags uint32,
	pathCount *uint32,
	paths []displayConfigPathInfo,
	modeCount *uint32,
	modes []displayConfigModeInfo,
) (int32, error) {
	index := f.queryCalls
	f.queryCalls++
	status := errorSuccess
	if len(f.queryStatuses) > 0 {
		if index >= len(f.queryStatuses) {
			index = len(f.queryStatuses) - 1
		}
		status = f.queryStatuses[index]
	}
	if status != errorSuccess {
		return status, nil
	}
	if int(*pathCount) < len(f.targets) {
		return errorInsufficientBuffer, nil
	}
	for i, target := range f.targets {
		paths[i].TargetInfo.ID = target.id
		paths[i].TargetInfo.AdapterID = luid{LowPart: 0x1234, HighPart: 0}
	}
	*pathCount = uint32(len(f.targets))
	return errorSuccess, nil
}

func (f *fakeCCD) displayConfigGetDeviceInfo(request *displayConfigTargetDeviceName) (int32, error) {
	f.infoCalls++
	if request.Header.Type != displayConfigDeviceInfoGetTargetName {
		return errorInvalidParameter, nil
	}
	if request.Header.Size != uint32(unsafe.Sizeof(displayConfigTargetDeviceName{})) {
		return errorInvalidParameter, nil
	}
	for _, target := range f.targets {
		if target.id != request.Header.ID {
			continue
		}
		if target.status != errorSuccess {
			return target.status, nil
		}
		writeUTF16(request.MonitorFriendlyDeviceName[:], target.friendly)
		writeUTF16(request.MonitorDevicePath[:], target.devicePath)
		return errorSuccess, nil
	}
	return errorInvalidParameter, nil
}

func writeUTF16(dst []uint16, value string) {
	for i, r := range []rune(value) {
		if i >= len(dst)-1 {
			break
		}
		dst[i] = uint16(r)
	}
}

// fakeNamer is the friendly-name seam the fake desktop supplies, so the enumeration
// can be tested with names, without names, and with a lookup that fails outright.
type fakeNamer struct {
	names map[string]string
	err   error
	calls int
}

func (f *fakeNamer) friendlyNames() (map[string]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.names, nil
}
