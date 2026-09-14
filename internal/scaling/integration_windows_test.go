package scaling

import (
	"fmt"
	"os"
	"testing"
	"unsafe"

	"github.com/Alien7666/change_resolution/internal/domain"
)

// RUN_NVAPI_INTEGRATION is a second, separate opt-in gate and never RUN_DISPLAY_INTEGRATION.
// That variable promises "read-only enumeration and CDS_TEST"; driving a different
// vendor API under it would empty the promise rather than extend it.
//
// What this gate authorises is exactly two things: reads, and the wrong-version
// negative control below. It does not authorise a write of any kind — not with the
// validate-only flag, not with a payload read back unchanged — because a set is a
// machine-wide operation that can renumber \\.\DISPLAYn on a GPU it was not even
// addressed to. Verifying a write is a supervised manual step before a release, not
// something a test run can stumble into. TestNoHardwareTestCanReachTheDisplayConfigWrite
// in nvapi_windows_test.go holds this file to that.
//
// Neither CI workflow sets this variable, and neither needs editing: go test ./...
// picks the package up on its own and every test here skips without it.
const nvapiIntegrationGate = "RUN_NVAPI_INTEGRATION"

func requireNvapiHardware(t *testing.T) *windowsNVAPI {
	t.Helper()
	if os.Getenv(nvapiIntegrationGate) != "1" {
		t.Skipf("set %s=1 to run the read-only NVAPI hardware checks", nvapiIntegrationGate)
	}
	api, err := loadNVAPI()
	if err != nil {
		t.Fatalf("load NVAPI: %v", err)
	}
	if callStatus := api.initialize(); callStatus != statusOK {
		t.Fatalf("NvAPI_Initialize: %s", api.errorMessage(callStatus))
	}
	t.Cleanup(func() {
		if callStatus := api.unload(); callStatus != statusOK {
			t.Logf("NvAPI_Unload: %s", api.errorMessage(callStatus))
		}
	})
	return api
}

// The three-pass read is the one part of this package no fake can exercise: the
// counting, the per-path allocation and the pointer binding have no branches a unit
// test could get wrong, and every one of them only runs against a real driver. This
// test is what covers it.
func TestNvapiIntegrationReadsTheLiveDisplayConfiguration(t *testing.T) {
	api := requireNvapiHardware(t)

	native, callStatus := api.readNative(pathInfoVersion(), advTargetInfoVersion())
	if callStatus != statusOK {
		t.Fatalf("NvAPI_DISP_GetDisplayConfig: %s", api.errorMessage(callStatus))
	}
	if native.count == 0 || len(native.paths) == 0 {
		t.Fatal("the driver reported no display paths")
	}
	t.Logf("pathInfoCount = %d", native.count)

	names := gdiNamesByDisplayID(api)
	targets := 0
	for pathIndex := range native.paths {
		path := &native.paths[pathIndex]
		mode := native.srcModes[pathIndex]
		t.Logf("path %d: sourceId=%d targetInfoCount=%d desktop=%dx%d@%dbpp at (%d,%d)",
			pathIndex, path.SourceID, path.TargetInfoCount,
			mode.Resolution.Width, mode.Resolution.Height, mode.Resolution.ColorDepth,
			mode.PosX, mode.PosY)

		if int(path.TargetInfoCount) != len(native.targets[pathIndex]) {
			t.Fatalf("path %d reported %d targets but %d were bound",
				pathIndex, path.TargetInfoCount, len(native.targets[pathIndex]))
		}
		for targetIndex := range native.targets[pathIndex] {
			target := native.targets[pathIndex][targetIndex]
			detail := native.details[pathIndex][targetIndex]
			targets++

			// The driver writing a plausible refresh rate into the advanced target info
			// is the practical proof that the pointer binding reached the right bytes:
			// an unbound or misaligned details array would come back as it was left.
			if detail.Version != advTargetInfoVersion() {
				t.Errorf("target %d.%d came back with version %#08x, want %#08x",
					pathIndex, targetIndex, detail.Version, advTargetInfoVersion())
			}
			value := decodeValue(detail.Scaling)
			t.Logf("  target %d: displayId=%#08x targetId=%d name=%q scaling=%d (mode=%d by=%d) refresh=%.3f Hz",
				targetIndex, target.DisplayID, target.TargetID, names[target.DisplayID],
				detail.Scaling, value.Mode, value.By, float64(detail.RefreshRate1K)/1000)
		}
	}
	if targets == 0 {
		t.Fatal("no display target was reported")
	}
}

// The negative control is what separates "the read did not fail, so the version is
// probably right" from "the driver rejects every version but this one". It drives the
// production sequence with a deliberately wrong stamp — once with the right size and a
// wrong version number, once with the right version number and a wrong size — and both
// must come back NVAPI_INCOMPATIBLE_STRUCT_VERSION.
func TestNvapiIntegrationRejectsADeliberatelyWrongStructVersion(t *testing.T) {
	api := requireNvapiHardware(t)

	tests := []struct {
		name        string
		pathVersion uint32
	}{
		{name: "right size, wrong version number", pathVersion: makeVersion(unsafe.Sizeof(pathInfo{}), 9)},
		{name: "right version number, wrong size", pathVersion: makeVersion(40, 2)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, callStatus := api.readNative(tt.pathVersion, advTargetInfoVersion())
			if callStatus != statusIncompatibleStructVersion {
				t.Fatalf("version %#08x returned %s, want NVAPI_INCOMPATIBLE_STRUCT_VERSION (-9)",
					tt.pathVersion, api.errorMessage(callStatus))
			}
			t.Logf("version %#08x rejected with %s", tt.pathVersion, api.errorMessage(callStatus))
		})
	}

	// And the stamp the production code uses is accepted, so the rejections above are
	// about the version and not about the call being malformed some other way.
	if _, callStatus := api.readNative(pathInfoVersion(), advTargetInfoVersion()); callStatus != statusOK {
		t.Fatalf("the production version stamp was rejected: %s", api.errorMessage(callStatus))
	}
}

// Everything above talks to the binding directly. This drives the shipping entry
// point instead, so the ANSI name boundary, the displayId cache, the exactly-one-target
// rule and the read-back all run against the real driver exactly as the tool runs them.
// It reads and nothing else.
func TestNvapiIntegrationReadsScalingThroughTheProductionController(t *testing.T) {
	api := requireNvapiHardware(t)

	deviceName, displayID := someNvidiaDisplay(t, api)
	identity := domain.MonitorIdentity{
		InstancePath: "nvapi-integration-probe",
		Label:        deviceName,
	}

	resolved := 0
	controller := NewWindowsController(func(domain.MonitorIdentity) (domain.Target, error) {
		resolved++
		return domain.Target{DeviceName: deviceName, Identity: identity}, nil
	})
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Logf("Close: %v", err)
		}
	})

	if availability := controller.Probe(); !availability.Available {
		t.Fatalf("Probe reported unavailable: %s (%v)", availability.Reason, availability.Err)
	}

	state, err := controller.Read(identity)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if state.DisplayID != displayID {
		t.Fatalf("Read resolved displayId %#08x, want %#08x", state.DisplayID, displayID)
	}
	t.Logf("%s -> displayId=%#08x scaling raw=%d mode=%d by=%d",
		deviceName, state.DisplayID, state.Effective.Raw, state.Effective.Mode, state.Effective.By)

	// A second read must reuse the cached displayId, because the cache is what lets
	// every later operation address the monitor without touching a GDI name again.
	if _, err := controller.Read(identity); err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("the GDI name was resolved %d times across two reads, want once", resolved)
	}
}

// gdiNamesByDisplayID maps what the driver calls a display back to what GDI calls it.
// Asking for names the machine does not have is a read that answers "no", which is
// how the mapping is discovered without enumerating anything else.
func gdiNamesByDisplayID(api *windowsNVAPI) map[uint32]string {
	names := make(map[uint32]string)
	for index := 1; index <= 16; index++ {
		name := fmt.Sprintf(`\\.\DISPLAY%d`, index)
		displayID, callStatus := api.displayIDByName(name)
		if callStatus == statusOK && displayID != 0 {
			names[displayID] = name
		}
	}
	return names
}

func someNvidiaDisplay(t *testing.T, api *windowsNVAPI) (string, uint32) {
	t.Helper()
	native, callStatus := api.readNative(pathInfoVersion(), advTargetInfoVersion())
	if callStatus != statusOK {
		t.Fatalf("NvAPI_DISP_GetDisplayConfig: %s", api.errorMessage(callStatus))
	}
	onAPath := make(map[uint32]int)
	for pathIndex := range native.targets {
		for _, target := range native.targets[pathIndex] {
			onAPath[target.DisplayID]++
		}
	}
	for index := 1; index <= 16; index++ {
		name := fmt.Sprintf(`\\.\DISPLAY%d`, index)
		displayID, callStatus := api.displayIDByName(name)
		if callStatus == statusOK && displayID != 0 && onAPath[displayID] == 1 {
			return name, displayID
		}
	}
	t.Skip("no GDI display name maps to exactly one NVIDIA display path on this machine")
	return "", 0
}
