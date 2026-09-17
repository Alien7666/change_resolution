package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/config"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/process"
)

type aspectFilter string

const (
	aspectAll           aspectFilter = "全部"
	aspectFourByThree   aspectFilter = "4:3"
	aspectSixteenByNine aspectFilter = "16:9"
	aspectSixteenByTen  aspectFilter = "16:10"
	aspectOther         aspectFilter = "其他"
)

// monitorRow is one attached monitor the settings dialog may show. A row with Reason
// remains visible so a missing mode catalogue is explained instead of hidden.
type monitorRow struct {
	Target         domain.Target
	CurrentMode    domain.Mode
	HasCurrentMode bool
	Primary        bool
	ModelWasUnique bool
	Reason         string
	modes          []domain.Mode
}

// modeRow groups all refresh rates for one resolution. Selection is still stored as a
// complete domain.Mode, because a profile must not silently pick a different refresh
// rate after a driver update.
type modeRow struct {
	Width                     uint32
	Height                    uint32
	Aspect                    string
	RefreshRates              []uint32
	HighestRefresh            uint32
	Current                   bool
	Native                    bool
	FullScreenScalingReminder bool
}

type settingsModel struct {
	displays display.Controller
	lister   process.Lister
	save     func(config.File) error
	firstRun bool

	// readScaling is owned by Provider. The dialog never constructs or writes through a
	// scaling controller; it asks for one fresh, read-only measurement whenever its
	// selected monitor changes, including first run where no Session exists.
	readScaling func(domain.MonitorIdentity) app.ScalingSnapshot
	scaling     app.ScalingSnapshot

	draft           domain.Profile
	legacyPrefilled bool
	monitorRows     []monitorRow
	modeRows        []modeRow
	processNames    []string
	processErr      error
	selectedMonitor int
	catalogueReady  bool
}

// newSettingsModel builds dialog-only state. Refresh is its only source read; changing
// a selection reads from the cache and never touches a display or GPU setting.
func newSettingsModel(
	displays display.Controller,
	lister process.Lister,
	profile domain.Profile,
	scalingView app.ScalingSnapshot,
	firstRun bool,
) *settingsModel {
	model := &settingsModel{
		displays:        displays,
		lister:          lister,
		save:            config.Save,
		scaling:         scalingView,
		firstRun:        firstRun,
		draft:           profile.Copy(),
		selectedMonitor: -1,
	}
	if profile.Monitor.InstancePath == "" && profile.Monitor.HardwareID == "" {
		model.draft.RestoreDelay = domain.LegacySeedProfile().RestoreDelay
	}
	return model
}

// Refresh re-enumerates read-only sources for this dialog. The results stay cached
// until the user explicitly refreshes or opens a new dialog.
func (m *settingsModel) Refresh() error {
	targets, err := m.displays.Targets()
	if err != nil {
		m.invalidateCatalogue()
		return err
	}
	layout, layoutErr := m.displays.CurrentLayout()

	counts := hardwareCounts(targets)
	rows := make([]monitorRow, 0, len(targets))
	for _, target := range targets {
		row := monitorRow{
			Target:         target,
			ModelWasUnique: counts[strings.ToLower(target.Identity.HardwareID)] == 1,
		}
		if layoutErr != nil {
			row.Reason = fmt.Sprintf("無法讀取目前顯示配置：%v", layoutErr)
		} else if state, ok := layout.Find(target.DeviceName); ok {
			row.CurrentMode, row.HasCurrentMode, row.Primary = state.Mode, true, state.Primary
		} else {
			row.Reason = "目前顯示配置缺少這台顯示器"
		}
		modes, modesErr := m.displays.EnumModes(target)
		if modesErr != nil {
			row.Reason = fmt.Sprintf("無法列舉可用模式：%v", modesErr)
		} else if len(modes) == 0 {
			row.Reason = "此顯示器沒有可用模式"
		} else {
			row.modes = append([]domain.Mode(nil), modes...)
		}
		rows = append(rows, row)
	}
	m.monitorRows = rows
	m.selectedMonitor = m.findSelectedMonitor()
	m.modeRows = nil
	m.catalogueReady = layoutErr == nil
	if m.selectedMonitor >= 0 {
		m.rebuildModeRows(m.selectedMonitor)
	}

	m.processNames, m.processErr = nil, nil
	if m.lister != nil {
		m.processNames, m.processErr = m.lister.Names()
	}

	if m.firstRun && m.selectedMonitor < 0 && emptyMonitor(m.draft.Monitor) {
		m.prefillLegacy(targets, counts)
	}
	m.refreshSelectedScaling()
	return nil
}

func (m *settingsModel) MonitorRows() []monitorRow {
	return append([]monitorRow(nil), m.monitorRows...)
}

func (m *settingsModel) ModeRows(filter aspectFilter) []modeRow {
	rows := make([]modeRow, 0, len(m.modeRows))
	for _, row := range m.modeRows {
		if !matchesAspectFilter(row.Aspect, filter) {
			continue
		}
		row.RefreshRates = append([]uint32(nil), row.RefreshRates...)
		rows = append(rows, row)
	}
	return rows
}

func (m *settingsModel) SelectMonitor(index int) error {
	if index < 0 || index >= len(m.monitorRows) {
		return fmt.Errorf("顯示器索引 %d 不存在", index)
	}
	row := m.monitorRows[index]
	sameMonitor := sameInstancePath(m.draft.Monitor.InstancePath, row.Target.Identity.InstancePath)
	m.draft.Monitor = domain.MonitorIdentity{
		InstancePath:   row.Target.Identity.InstancePath,
		HardwareID:     row.Target.Identity.HardwareID,
		ModelWasUnique: row.ModelWasUnique,
		Label:          row.Target.Identity.Label,
	}
	if !sameMonitor {
		m.draft.FallbackMode = nil
		m.draft.GameMode = domain.Mode{}
	}
	m.selectedMonitor = index
	m.rebuildModeRows(index)
	m.refreshSelectedScaling()
	return nil
}

func (m *settingsModel) refreshSelectedScaling() {
	if m.readScaling == nil || m.selectedMonitor < 0 || m.selectedMonitor >= len(m.monitorRows) {
		m.scaling = app.ScalingSnapshot{}
		return
	}
	m.scaling = m.readScaling(m.monitorRows[m.selectedMonitor].Target.Identity)
}

// SelectResolution chooses a resolution's highest reported refresh rate. The caller
// can then use SelectMode to commit any other reported refresh rate.
func (m *settingsModel) SelectResolution(width, height uint32) error {
	for _, row := range m.modeRows {
		if row.Width == width && row.Height == height {
			return m.SelectMode(domain.Mode{
				Width: width, Height: height, RefreshHz: row.HighestRefresh, BitsPerPixel: 32,
			})
		}
	}
	return fmt.Errorf("顯示器沒有回報 %d × %d", width, height)
}

func (m *settingsModel) SelectMode(mode domain.Mode) error {
	for _, row := range m.modeRows {
		if row.Width != mode.Width || row.Height != mode.Height {
			continue
		}
		for _, refresh := range row.RefreshRates {
			if refresh == mode.RefreshHz && mode.BitsPerPixel == 32 {
				m.draft.GameMode = mode
				return nil
			}
		}
	}
	return fmt.Errorf("顯示器沒有回報 %s", domain.ModeLabel(mode))
}

func (m *settingsModel) SetProcessName(name string) { m.draft.ProcessName = name }

func (m *settingsModel) UseManualOnly() { m.draft.ProcessName = "" }

func (m *settingsModel) ProcessNames(query string) []string {
	query = strings.ToLower(query)
	var names []string
	for _, name := range m.processNames {
		if strings.Contains(strings.ToLower(name), query) {
			names = append(names, name)
		}
	}
	return names
}

func (m *settingsModel) ProcessError() error { return m.processErr }

func (m *settingsModel) Draft() domain.Profile { return m.draft.Copy() }

func (m *settingsModel) LegacyPrefilled() bool { return m.legacyPrefilled }

// SaveReady reports whether the current read-only catalogue still proves that this
// draft names one attached monitor and one exact mode it reported. This is picker
// readiness, not a second copy of configuration-schema validation.
func (m *settingsModel) SaveReady() (bool, string) {
	if !m.catalogueReady {
		return false, "尚未成功讀取目前的顯示器資訊"
	}
	if m.selectedMonitor < 0 || m.selectedMonitor >= len(m.monitorRows) {
		return false, "請選擇目前連線的顯示器"
	}
	row := m.monitorRows[m.selectedMonitor]
	if !sameInstancePath(m.draft.Monitor.InstancePath, row.Target.Identity.InstancePath) {
		return false, "請重新選擇目前連線的顯示器"
	}
	if !row.HasCurrentMode {
		return false, "目前顯示配置沒有這台顯示器"
	}
	if len(row.modes) == 0 {
		if row.Reason != "" {
			return false, row.Reason
		}
		return false, "此顯示器沒有可用模式"
	}
	if !containsMode(row.modes, m.draft.GameMode) {
		return false, "請從目前顯示器回報的模式中選擇完整模式"
	}
	return true, ""
}

// Save delegates all schema validation and atomic persistence to config.Save. It only
// returns a profile after a successful write, so dialog wiring can call Provider.Replace
// only on that success path.
func (m *settingsModel) Save() (domain.Profile, error) {
	if ready, reason := m.SaveReady(); !ready {
		return domain.Profile{}, fmt.Errorf("設定尚未可儲存：%s", reason)
	}
	if err := m.save(config.FromProfile(m.draft)); err != nil {
		return domain.Profile{}, err
	}
	return m.Draft(), nil
}

func (m *settingsModel) findSelectedMonitor() int {
	if m.draft.Monitor.InstancePath == "" {
		return -1
	}
	for index, row := range m.monitorRows {
		if sameInstancePath(m.draft.Monitor.InstancePath, row.Target.Identity.InstancePath) {
			return index
		}
	}
	return -1
}

func (m *settingsModel) invalidateCatalogue() {
	m.catalogueReady = false
	m.monitorRows = nil
	m.modeRows = nil
	m.selectedMonitor = -1
	m.legacyPrefilled = false
}

func (m *settingsModel) rebuildModeRows(index int) {
	row := m.monitorRows[index]
	m.modeRows = groupedModeRows(row.modes, row.CurrentMode, row.HasCurrentMode)
}

func (m *settingsModel) prefillLegacy(_ []domain.Target, counts map[string]int) {
	legacy := domain.LegacySeedProfile()
	key := strings.ToLower(legacy.Monitor.HardwareID)
	if counts[key] != 1 {
		return
	}
	for index, row := range m.monitorRows {
		if !strings.EqualFold(row.Target.Identity.HardwareID, legacy.Monitor.HardwareID) {
			continue
		}
		if !containsMode(row.modes, legacy.GameMode) {
			return
		}
		m.draft = legacy.Copy()
		m.selectedMonitor = index
		m.draft.Monitor = domain.MonitorIdentity{
			InstancePath:   row.Target.Identity.InstancePath,
			HardwareID:     row.Target.Identity.HardwareID,
			ModelWasUnique: true,
			Label:          row.Target.Identity.Label,
		}
		m.rebuildModeRows(index)
		m.legacyPrefilled = true
		return
	}
}

func groupedModeRows(modes []domain.Mode, current domain.Mode, hasCurrent bool) []modeRow {
	native, hasNative := domain.NativeMode(modes)
	groups := make(map[[2]uint32]*modeRow)
	for _, mode := range modes {
		key := [2]uint32{mode.Width, mode.Height}
		row := groups[key]
		if row == nil {
			row = &modeRow{Width: mode.Width, Height: mode.Height, Aspect: domain.AspectLabel(mode.Width, mode.Height)}
			groups[key] = row
		}
		row.RefreshRates = append(row.RefreshRates, mode.RefreshHz)
		if hasCurrent && mode == current {
			row.Current = true
		}
	}
	rows := make([]modeRow, 0, len(groups))
	for _, row := range groups {
		sort.Slice(row.RefreshRates, func(i, j int) bool { return row.RefreshRates[i] > row.RefreshRates[j] })
		row.HighestRefresh = row.RefreshRates[0]
		if hasNative && row.Width == native.Width && row.Height == native.Height {
			row.Native = true
			row.FullScreenScalingReminder = !sameAspect(row.Width, row.Height, native.Width, native.Height)
		} else if hasNative {
			row.FullScreenScalingReminder = !sameAspect(row.Width, row.Height, native.Width, native.Height)
		}
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return domain.LargerMode(
			domain.Mode{Width: rows[i].Width, Height: rows[i].Height, RefreshHz: rows[i].HighestRefresh},
			domain.Mode{Width: rows[j].Width, Height: rows[j].Height, RefreshHz: rows[j].HighestRefresh},
		)
	})
	return rows
}

func hardwareCounts(targets []domain.Target) map[string]int {
	counts := make(map[string]int, len(targets))
	for _, target := range targets {
		counts[strings.ToLower(target.Identity.HardwareID)]++
	}
	return counts
}

func emptyMonitor(identity domain.MonitorIdentity) bool {
	return identity.InstancePath == "" && identity.HardwareID == ""
}

func sameInstancePath(left, right string) bool {
	return left != "" && right != "" && strings.EqualFold(left, right)
}

func containsMode(modes []domain.Mode, want domain.Mode) bool {
	for _, mode := range modes {
		if mode == want {
			return true
		}
	}
	return false
}

func sameAspect(width, height, otherWidth, otherHeight uint32) bool {
	return uint64(width)*uint64(otherHeight) == uint64(otherWidth)*uint64(height)
}

func matchesAspectFilter(aspect string, filter aspectFilter) bool {
	switch filter {
	case "", aspectAll:
		return true
	case aspectFourByThree, aspectSixteenByNine, aspectSixteenByTen:
		return aspect == string(filter)
	case aspectOther:
		return aspect != string(aspectFourByThree) && aspect != string(aspectSixteenByNine) && aspect != string(aspectSixteenByTen)
	default:
		return false
	}
}
