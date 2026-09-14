package domain

import "time"

// MaxDimension is the one sanity bound on a display dimension the whole tool shares.
// It is far beyond any real monitor, so nothing a driver reports is refused by it,
// while rejecting anything past it keeps every arrangement shift inside the int32
// coordinates Win32 uses and turns a display the tool failed to read (a zero-sized
// or absurd mode) into a refusal to act.
//
// It lives in domain because both the arrangement planner in internal/display and
// the configuration parser in internal/config have to validate against the same
// number, and neither may import the other.
const MaxDimension = 1 << 16

type Mode struct {
	Width        uint32
	Height       uint32
	RefreshHz    uint32
	BitsPerPixel uint32
}

// MonitorIdentity is how a configured monitor is recognised again after a reboot, a
// cable swap or a Windows renumbering. \\.\DISPLAYn is none of those things — it is
// assigned dynamically — so it is never stored here.
//
// The two keys are a ladder, tried in order and never guessed at:
//
//   - InstancePath is the primary key: the device interface path reported by
//     EnumDisplayDevicesW with EDD_GET_DEVICE_INTERFACE_NAME, which carries the
//     graphics card's output port (the UID… segment) and therefore distinguishes two
//     units of the same model. Compared whole and case-insensitively, never as a
//     prefix. It survives a reboot and a renumbering; it does not survive moving the
//     cable to another port.
//   - HardwareID is the secondary key and identifies a model, not a unit. It exists
//     to recover from exactly that cable move, and is only consulted when the
//     primary key misses and ModelWasUnique is true.
//
// ModelWasUnique records whether this HardwareID matched exactly one attached
// monitor at the moment the user made the selection. When it is false the secondary
// rung is disabled permanently for this profile: with two of the model configured, a
// single hit most likely means the other one is asleep or unplugged, not that this is
// the one the user wanted, and applying a mode to the wrong screen is worse than
// asking them to reselect.
//
// Label is for humans only and never participates in a comparison.
type MonitorIdentity struct {
	InstancePath   string
	HardwareID     string
	ModelWasUnique bool
	Label          string
}

// MatchLevel reports which rung of the identity ladder produced a Target, so the UI
// can tell the user when a profile was recovered through the secondary key rather
// than recognised outright.
type MatchLevel uint8

const (
	MatchNone MatchLevel = iota
	MatchInstancePath
	MatchHardwareID
)

// Target is a resolved monitor. DeviceName is the adapter name Win32 accepts
// (\\.\DISPLAYn) and is valid only for the read that produced it — it is re-resolved
// before every operation rather than stored.
type Target struct {
	DeviceName string
	Identity   MonitorIdentity
	MatchedBy  MatchLevel
}

type Profile struct {
	Name               string
	Monitor            MonitorIdentity
	GameMode           Mode
	FallbackNativeMode Mode
	ProcessName        string
	RestoreDelay       time.Duration
}

// LegacySeedProfile is the configuration the tool shipped with, demoted to a seed:
// its only job is to pre-fill the first-run wizard so the machine this tool was
// written for needs no retyping. A shipping session never runs on it — it runs on
// whatever the user saved.
//
// The identity it carries is deliberately half-filled; see
// TestLegacySeedProfileHasNoInstancePathAndClaimsAUniqueModel for why an empty
// InstancePath next to ModelWasUnique: true is the combination that keeps the
// original MONITOR\XMI27B2 match working.
func LegacySeedProfile() Profile {
	return Profile{
		Name: "Mi Monitor 4:3",
		Monitor: MonitorIdentity{
			HardwareID:     `MONITOR\XMI27B2`,
			ModelWasUnique: true,
			Label:          "Mi Monitor (XMI27B2)",
		},
		GameMode:           Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		FallbackNativeMode: Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		ProcessName:        "VALORANT-Win64-Shipping.exe",
		RestoreDelay:       3 * time.Second,
	}
}
