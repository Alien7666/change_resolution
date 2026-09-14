package display

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Alien7666/change_resolution/internal/domain"
)

var (
	// ErrTargetNotFound reports that no attached monitor matched the configured
	// identity on any rung of the ladder. It is not a configuration error: a monitor
	// that is asleep or switched to another input is simply not in the list, and the
	// profile is kept so that re-reading can find it again.
	ErrTargetNotFound = errors.New("target display not found")

	// ErrTargetAmbiguous reports that the configured identity hit more than one
	// attached monitor, which the secondary key can do because it names a model
	// rather than a unit. Taking the first would apply a mode to whichever screen
	// Windows enumerated first, so the tool refuses and asks the user to reselect.
	ErrTargetAmbiguous = errors.New("設定的顯示器同時命中多台")

	// ErrTargetMirrored reports that the adapter the identity resolved to still
	// drives another attached monitor, which is what clone and mirror modes do.
	// ChangeDisplaySettingsExW takes the adapter, so the tool cannot change one of
	// the pair without changing both.
	ErrTargetMirrored = errors.New("解析出的顯示裝置同時驅動另一台顯示器")

	// ErrModeNotSupported reports that the CDS_TEST pre-flight refused the mode, so
	// the target cannot be driven at it. Callers use errors.Is to separate an
	// unsupported mode from an apply that merely failed once.
	ErrModeNotSupported = errors.New("display mode not supported")

	// ErrLayoutNotVerified reports that the desktop read back after an apply is not
	// the arrangement that was planned. Every call returned DISP_CHANGE_SUCCESSFUL,
	// so the mode change did happen; what could not be confirmed is that the desktop
	// it produced is the one that was proved safe.
	ErrLayoutNotVerified = errors.New("display layout was not applied as planned")

	// ErrLayoutPartlyApplied reports that an apply failed and putting the displays it
	// had already changed back failed too. The desktop is left in an arrangement
	// neither the tool nor the user chose, which the user has to be told rather than
	// be told the operation simply failed and changed nothing.
	ErrLayoutPartlyApplied = errors.New("display layout was left partly applied")
)

type Controller interface {
	// ResolveTarget finds the one attached monitor the configured identity names.
	// It answers with a refusal rather than a guess: nothing, several, or a
	// mirrored adapter are all errors, never a monitor picked out of a list.
	ResolveTarget(identity domain.MonitorIdentity) (domain.Target, error)
	CurrentMode(target domain.Target) (domain.Mode, error)
	CurrentLayout() (domain.Layout, error)
	TestMode(target domain.Target, mode domain.Mode) error
	ApplyLayout(plan domain.LayoutPlan) error
}

type nativeAPI interface {
	// listTargets reports one target per attached monitor, not per adapter, each
	// carrying the full identity read from Win32. A cloned or mirrored adapter
	// drives several monitors, so several targets can share one DeviceName; that
	// repetition is the only evidence a caller has that the adapter it resolved is
	// not the tool's alone to change.
	listTargets() ([]domain.Target, error)
	currentMode(deviceName string) (domain.Mode, error)
	currentLayout() (domain.Layout, error)
	testMode(deviceName string, mode domain.Mode) error
	applyLayout(plan domain.LayoutPlan) error
}

type controller struct {
	native nativeAPI
}

func newController(native nativeAPI) *controller {
	return &controller{native: native}
}

// ResolveTarget enumerates the attached monitors and hands them to the pure
// matcher. The enumeration is redone on every call rather than cached: \\.\DISPLAYn
// is assigned dynamically, so a name read earlier may by now belong to another
// screen.
func (c *controller) ResolveTarget(identity domain.MonitorIdentity) (domain.Target, error) {
	targets, err := c.native.listTargets()
	if err != nil {
		return domain.Target{}, err
	}
	return resolveIdentity(targets, identity)
}

func (c *controller) CurrentMode(target domain.Target) (domain.Mode, error) {
	return c.native.currentMode(target.DeviceName)
}

// CurrentLayout reads every attached display's mode and desktop position. It is a
// read: planning a contiguous desktop needs the coordinates of the displays the
// tool will move, not just the target's mode.
func (c *controller) CurrentLayout() (domain.Layout, error) {
	return c.native.currentLayout()
}

// TestMode runs the CDS_TEST pre-flight. Every rejection is wrapped with
// ErrModeNotSupported so callers can gate on the sentinel instead of on the
// wording of the underlying Win32 diagnostic.
func (c *controller) TestMode(target domain.Target, mode domain.Mode) error {
	if err := c.native.testMode(target.DeviceName, mode); err != nil {
		return fmt.Errorf("%w: %w", ErrModeNotSupported, err)
	}
	return nil
}

// ApplyLayout applies a whole arrangement. Callers build the plan with
// PlanModeChange or PlanRestore, which refuse to produce a plan they cannot prove
// safe, so a half-considered arrangement never reaches the driver. The plan is
// handed to the native layer whole because that layer owns the apply order, the
// rollback of a partial apply, and the check that the desktop ended up where the
// plan said it would.
func (c *controller) ApplyLayout(plan domain.LayoutPlan) error {
	return c.native.applyLayout(plan)
}

// resolveIdentity picks the one attached monitor a configured identity names, or
// explains why it cannot. It never takes the first of several matches: this package
// changes a display mode, and changing the wrong screen is worse than changing none.
//
// The ladder is tried from precise to loose, and no rung guesses:
//
//  1. InstancePath, the device interface path, compared whole and case-insensitively.
//     It carries the graphics card output port, so it tells two units of one model
//     apart. It survives a reboot and a Windows renumbering of \\.\DISPLAYn; it does
//     not survive moving the cable to another port.
//  2. HardwareID, compared on the model portion, and only when the profile recorded
//     that the model was unique at the moment the user chose it. This rung exists to
//     recover from exactly that cable move.
//
// A rung that matches nothing falls through to the next; a rung that matches several
// refuses with ErrTargetAmbiguous rather than choosing. A match that survives both
// rungs still has to pass the clone check in acceptTarget.
func resolveIdentity(targets []domain.Target, want domain.MonitorIdentity) (domain.Target, error) {
	path := want.InstancePath
	if path != "" {
		matches := matchingTargets(targets, func(target domain.Target) bool {
			return target.Identity.InstancePath != "" &&
				strings.EqualFold(target.Identity.InstancePath, path)
		})
		switch len(matches) {
		case 1:
			return acceptTarget(targets, matches[0], domain.MatchInstancePath)
		case 0: // fall through to the secondary key
		default:
			return domain.Target{}, ambiguousTarget(path, targets, matches)
		}
	}

	model := modelOf(want.HardwareID)
	if model == "" {
		return domain.Target{}, fmt.Errorf(
			"%w: 設定中沒有可用來辨識顯示器的裝置介面路徑或硬體 ID，請重新選擇顯示器", ErrTargetNotFound)
	}
	matches := matchingTargets(targets, func(target domain.Target) bool {
		return target.Identity.HardwareID != "" &&
			strings.EqualFold(modelOf(target.Identity.HardwareID), model)
	})
	if !want.ModelWasUnique {
		// The rung is disabled for this profile, and that is a decision the user made
		// by configuring it while two of the model were attached. A single hit now
		// most likely means the other one is asleep, on another input or unplugged,
		// not that this is the unit they picked.
		return domain.Target{}, fmt.Errorf("%w: %s，而設定當下 %s 不只一台，硬體 ID 比對已停用，請重新選擇顯示器",
			ErrTargetNotFound, missingPathReason(path), model)
	}
	switch len(matches) {
	case 1:
		return acceptTarget(targets, matches[0], domain.MatchHardwareID)
	case 0:
		return domain.Target{}, fmt.Errorf("%w: 目前沒有連接 %s，%s",
			ErrTargetNotFound, model, missingPathReason(path))
	default:
		return domain.Target{}, ambiguousTarget(model, targets, matches)
	}
}

// missingPathReason words the primary key's failure the two ways it can happen, so a
// refusal never claims a path is offline when the profile never stored one.
func missingPathReason(path string) string {
	if path == "" {
		return "設定中沒有記錄顯示器的裝置介面路徑"
	}
	return "設定記錄的顯示器裝置介面路徑目前不在線上"
}

// acceptTarget is the last gate every rung passes through.
//
// \\.\DISPLAYn names the adapter, not the monitor, and it is the name
// ChangeDisplaySettingsExW takes. Under clone or mirror one adapter drives several
// monitors at once, so applying a mode through it would change every one of them.
// listTargets reports one target per monitor, so another target carrying the same
// DeviceName is precisely that situation, and the tool has no way to change one of
// the pair. Saying so beats changing a screen the user never configured.
func acceptTarget(targets []domain.Target, index int, level domain.MatchLevel) (domain.Target, error) {
	matched := targets[index]
	var others []domain.Target
	for i, target := range targets {
		if i != index && target.DeviceName == matched.DeviceName {
			others = append(others, target)
		}
	}
	if len(others) > 0 {
		return domain.Target{}, fmt.Errorf(
			"%w: %s 同時驅動 %s，工具無法只變更其中一台，請先關閉複製／鏡射顯示",
			ErrTargetMirrored, matched.DeviceName, describeMonitors(others))
	}
	matched.MatchedBy = level
	return matched, nil
}

func matchingTargets(targets []domain.Target, match func(domain.Target) bool) []int {
	var matches []int
	for i, target := range targets {
		if match(target) {
			matches = append(matches, i)
		}
	}
	return matches
}

// ambiguousTarget names every monitor the key hit. Reselecting is the only way out
// of this state, so the message has to say which screens are involved; a bare "命中
// 多台" would leave the user to guess which two of four monitors the tool meant.
func ambiguousTarget(key string, targets []domain.Target, matches []int) error {
	candidates := make([]domain.Target, 0, len(matches))
	for _, index := range matches {
		candidates = append(candidates, targets[index])
	}
	return fmt.Errorf("%w: %s 同時命中 %s，請重新選擇顯示器",
		ErrTargetAmbiguous, key, describeMonitors(candidates))
}

// describeMonitors identifies a monitor the two ways the user can act on: the
// \\.\DISPLAYn Windows shows them, and the interface path that tells two units of one
// model apart.
func describeMonitors(targets []domain.Target) string {
	described := make([]string, 0, len(targets))
	for _, target := range targets {
		name := target.DeviceName
		if name == "" {
			name = "（未知顯示裝置）"
		}
		switch {
		case target.Identity.InstancePath != "":
			name += "（" + target.Identity.InstancePath + "）"
		case target.Identity.HardwareID != "":
			name += "（" + target.Identity.HardwareID + "）"
		}
		described = append(described, name)
	}
	return strings.Join(described, "、")
}

// modelOf reduces a monitor device ID to the segments that name the model.
// EnumDisplayDevicesW reports a monitor's hardware ID as MONITOR\XMI27B2\0009 — on
// some systems with the device class GUID between the two — where everything past
// the model is instance detail that changes with the port the monitor is plugged
// into. A profile may have stored either spelling, so both sides of a comparison are
// reduced to the first two segments before they meet.
//
// This is the one place the secondary key is allowed to be loose, and it is loose in
// a bounded way: it compares two whole model names, never a prefix of one against
// the other.
func modelOf(hardwareID string) string {
	segments := strings.SplitN(hardwareID, `\`, 3)
	if len(segments) < 2 {
		return hardwareID
	}
	return segments[0] + `\` + segments[1]
}
