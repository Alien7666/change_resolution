//go:build windows

package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"
)

type settingsGates struct {
	Mode    bool
	Process bool
	Save    bool
	Reason  string
}

// settingsDialogFlow is the part of the dialog that decides when a later section is
// usable and when a successful write may replace the running profile. It contains no
// Walk objects, so the safety policy is tested without launching a window.
type settingsDialogFlow struct {
	model   *settingsModel
	replace func(domain.Profile) error
	// manualOnly records the one thing the draft cannot say for itself: that an
	// empty process name is a deliberate choice to watch nothing rather than a step
	// the user has not reached yet. Whether a process was chosen is read from the
	// draft, so no widget can leave the two disagreeing.
	manualOnly bool
	rebuilding bool
	accepted   bool
}

func newSettingsDialogFlow(model *settingsModel, replace func(domain.Profile) error) *settingsDialogFlow {
	return &settingsDialogFlow{model: model, replace: replace}
}

func (f *settingsDialogFlow) syncInitialProcessChoice() {
	// An existing profile with no process name is already watching nothing, and
	// reopening the dialog must not turn that settled choice back into an unfinished
	// step. A blank first run is the only case where nothing has been decided.
	if !f.model.firstRun || f.model.LegacyPrefilled() {
		f.manualOnly = strings.TrimSpace(f.model.Draft().ProcessName) == ""
	}
}

// processChosen reports whether the process step is settled. It is derived rather
// than tracked: a draft carrying a name is a choice however it got there, which is
// what stops the dialog refusing to save a configuration it is already holding.
func (f *settingsDialogFlow) processChosen() bool {
	return strings.TrimSpace(f.model.Draft().ProcessName) != "" || f.manualOnly
}

func (f *settingsDialogFlow) Gates() settingsGates {
	monitorSelected := f.model.selectedMonitor >= 0 && f.model.selectedMonitor < len(f.model.monitorRows)
	modeSelected := false
	if monitorSelected {
		modeSelected = containsMode(f.model.monitorRows[f.model.selectedMonitor].modes, f.model.Draft().GameMode)
	}
	ready, reason := f.model.SaveReady()
	// Every blocked gate carries the sentence that unblocks it. SaveReady returns an
	// empty reason once the picker is satisfied, so a draft held back only by the
	// process choice would otherwise disable the button and say nothing at all --
	// the control and its explanation have to come from one place or the dialog has
	// a dead end in it.
	if ready && !f.processChosen() {
		reason = missingProcessChoice
	}
	return settingsGates{
		Mode:    monitorSelected,
		Process: modeSelected,
		Save:    ready && f.processChosen(),
		Reason:  reason,
	}
}

func (f *settingsDialogFlow) SelectMonitor(index int) error {
	if f.rebuilding {
		return nil
	}
	return f.model.SelectMonitor(index)
}

func (f *settingsDialogFlow) SelectResolution(width, height uint32) error {
	if f.rebuilding {
		return nil
	}
	return f.model.SelectResolution(width, height)
}

func (f *settingsDialogFlow) SelectMode(mode domain.Mode) error {
	if f.rebuilding {
		return nil
	}
	return f.model.SelectMode(mode)
}

func (f *settingsDialogFlow) SetProcessName(name string) {
	if f.rebuilding {
		return
	}
	f.model.SetProcessName(name)
	if strings.TrimSpace(name) != "" {
		f.manualOnly = false
	}
}

func (f *settingsDialogFlow) UseManualOnly() {
	if f.rebuilding {
		return
	}
	f.model.UseManualOnly()
	f.manualOnly = true
}

func (f *settingsDialogFlow) clearProcessChoice() {
	if !f.rebuilding {
		f.manualOnly = false
	}
}

func (f *settingsDialogFlow) Save() error {
	gates := f.Gates()
	if !gates.Save {
		return fmt.Errorf("設定尚未可儲存：%s", gates.Reason)
	}
	profile, err := f.model.validatedProfile()
	if err != nil {
		return err
	}
	if f.replace == nil {
		return errors.New("沒有可套用設定的工作階段提供者")
	}
	if err := f.replace(profile); err != nil {
		return err
	}
	f.accepted = true
	return nil
}

func (f *settingsDialogFlow) Cancel()        { f.accepted = false }
func (f *settingsDialogFlow) Accepted() bool { return f.accepted }

func (f *settingsDialogFlow) withRebuild(action func()) {
	previous := f.rebuilding
	f.rebuilding = true
	defer func() { f.rebuilding = previous }()
	action()
}

// showSettingsDialog opens the same modal for first run and later edits. The model
// decides the data and performs the one config.Save call; replace runs only after that
// succeeds. Cancel and window-close paths never save.
func showSettingsDialog(owner walk.Form, model *settingsModel, replace func(domain.Profile) error) (bool, error) {
	if model == nil {
		return false, errors.New("settings: model must not be nil")
	}
	d := &settingsDialog{
		flow:         newSettingsDialogFlow(model, replace),
		aspect:       aspectAll,
		monitorTable: &monitorTableModel{},
		modeTable:    &modeTableModel{scaling: model.scaling},
	}
	refreshErr := model.Refresh()
	d.flow.syncInitialProcessChoice()
	if err := d.create(owner); err != nil {
		return false, err
	}
	defer d.dialog.Dispose()
	d.rebuild(refreshErr)
	d.dialog.Run()
	return d.flow.Accepted(), nil
}

type settingsDialog struct {
	flow   *settingsDialogFlow
	aspect aspectFilter

	dialog       *walk.Dialog
	hintLabel    *walk.TextLabel
	statusLabel  *walk.TextLabel
	monitorGroup *walk.GroupBox
	modeGroup    *walk.GroupBox
	processGroup *walk.GroupBox

	monitorView  *walk.TableView
	monitorTip   *walk.TextLabel
	modeView     *walk.TableView
	aspectBox    *walk.ComboBox
	refreshBox   *walk.ComboBox
	refreshLabel *walk.Label
	modeTip      *walk.TextLabel

	processSearch *walk.LineEdit
	processList   *walk.ListBox
	processEdit   *walk.LineEdit
	manualOnly    *walk.CheckBox
	processTip    *walk.TextLabel

	refreshButton *walk.PushButton
	saveButton    *walk.PushButton
	cancelButton  *walk.PushButton

	monitorTable *monitorTableModel
	modeTable    *modeTableModel
	processRows  []string
}

func (d *settingsDialog) create(owner walk.Form) error {
	title := "ResolutionTray 設定"
	hint := "依序選擇顯示器、顯示模式與要觀察的程序。這裡只讀取可用選項，不會變更顯示狀態。"
	cancelText := "取消"
	if d.flow.model.firstRun {
		title = "ResolutionTray 初次設定"
		hint = "第一次使用請依序完成三個步驟。儲存前不會建立設定檔，也不會變更顯示狀態。"
		cancelText = "稍後再設定"
	}

	err := dec.Dialog{
		AssignTo:  &d.dialog,
		Title:     title,
		Size:      dec.Size{Width: 680, Height: 680},
		MinSize:   dec.Size{Width: 560, Height: 460},
		FixedSize: false,
		Layout: dec.VBox{
			Margins: dec.Margins{Left: 12, Top: 12, Right: 12, Bottom: 12},
			Spacing: 8,
		},
		DefaultButton: &d.saveButton,
		CancelButton:  &d.cancelButton,
		Children: []dec.Widget{
			dec.TextLabel{AssignTo: &d.hintLabel, Text: hint, MinSize: dec.Size{Height: 32}},
			dec.GroupBox{
				AssignTo: &d.monitorGroup,
				Title:    "1. 顯示器",
				Layout:   dec.VBox{Margins: dec.Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 4},
				Children: []dec.Widget{
					dec.TableView{
						AssignTo: &d.monitorView, Model: d.monitorTable,
						AlternatingRowBG: true, MultiSelection: false,
						LastColumnStretched: true, MinSize: dec.Size{Height: 105},
						Columns: []dec.TableViewColumn{
							{Title: "名稱", Width: 150},
							{Title: "顯示裝置", Width: 105},
							{Title: "目前模式", Width: 155},
							{Title: "主要", Width: 45},
							{Title: "備註", Width: 145},
						},
						OnCurrentIndexChanged: d.onMonitorSelected,
					},
					dec.TextLabel{AssignTo: &d.monitorTip, Text: "請選擇一台顯示器", MinSize: dec.Size{Height: 24}},
				},
			},
			dec.GroupBox{
				AssignTo: &d.modeGroup,
				Title:    "2. 顯示模式",
				Layout:   dec.VBox{Margins: dec.Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 4},
				Children: []dec.Widget{
					dec.Composite{
						Layout: dec.HBox{MarginsZero: true, Spacing: 6},
						Children: []dec.Widget{
							dec.Label{Text: "比例："},
							dec.ComboBox{AssignTo: &d.aspectBox, Model: aspectFilterNames(), OnCurrentIndexChanged: d.onAspectChanged},
							dec.HSpacer{},
							dec.Label{AssignTo: &d.refreshLabel, Text: "刷新率："},
							dec.ComboBox{AssignTo: &d.refreshBox, Model: []string{}, OnCurrentIndexChanged: d.onRefreshChanged},
						},
					},
					dec.TableView{
						AssignTo: &d.modeView, Model: d.modeTable,
						AlternatingRowBG: true, MultiSelection: false,
						LastColumnStretched: true, MinSize: dec.Size{Height: 105},
						Columns: []dec.TableViewColumn{
							{Title: "解析度", Width: 120},
							{Title: "比例", Width: 70},
							{Title: "最高刷新率", Width: 100},
							{Title: "備註", Width: 260},
						},
						OnCurrentIndexChanged: d.onModeSelected,
					},
					dec.TextLabel{AssignTo: &d.modeTip, Text: "請選擇顯示模式", MinSize: dec.Size{Height: 24}},
				},
			},
			dec.GroupBox{
				AssignTo: &d.processGroup,
				Title:    "3. 自動恢復程序",
				Layout:   dec.VBox{Margins: dec.Margins{Left: 8, Top: 8, Right: 8, Bottom: 8}, Spacing: 4},
				Children: []dec.Widget{
					dec.LineEdit{AssignTo: &d.processSearch, CueBanner: "搜尋目前執行中的程序", OnTextChanged: d.onProcessSearch},
					dec.ListBox{AssignTo: &d.processList, Model: []string{}, MinSize: dec.Size{Height: 72}, OnCurrentIndexChanged: d.onProcessSelected},
					dec.LineEdit{AssignTo: &d.processEdit, CueBanner: "程序檔名，例如 game.exe", OnTextChanged: d.onProcessTextChanged},
					dec.CheckBox{AssignTo: &d.manualOnly, Text: "不觀察任何程序（只用手動切換）", OnCheckedChanged: d.onManualOnlyChanged},
					dec.TextLabel{AssignTo: &d.processTip, Text: "請選擇或輸入程序檔名", MinSize: dec.Size{Height: 20}},
				},
			},
			dec.TextLabel{AssignTo: &d.statusLabel, Text: "", MinSize: dec.Size{Height: 34}},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.PushButton{AssignTo: &d.refreshButton, Text: "重新整理", OnClicked: d.onRefresh},
					dec.HSpacer{},
					dec.PushButton{AssignTo: &d.saveButton, Text: "儲存", OnClicked: d.onSave},
					dec.PushButton{AssignTo: &d.cancelButton, Text: cancelText, OnClicked: d.onCancel},
				},
			},
		},
	}.Create(owner)
	if err != nil {
		return fmt.Errorf("create settings dialog: %w", err)
	}
	return nil
}

func (d *settingsDialog) rebuild(refreshErr error) {
	d.flow.withRebuild(func() {
		d.monitorTable.rows = d.flow.model.MonitorRows()
		d.monitorTable.PublishRowsReset()
		// The selected monitor may have changed since this table model was built. Keep
		// the 備註 column on the same fresh, provider-owned scaling read as settingsModel.
		d.modeTable.scaling = d.flow.model.scaling
		d.modeTable.rows = d.flow.model.ModeRows(d.aspect)
		d.modeTable.PublishRowsReset()

		_ = d.aspectBox.SetModel(aspectFilterNames())
		_ = d.aspectBox.SetCurrentIndex(aspectFilterIndex(d.aspect))
		_ = d.monitorView.SetCurrentIndex(d.flow.model.selectedMonitor)
		d.selectDraftModeRow()
		d.rebuildRefreshRates()
		d.rebuildProcesses()

		draft := d.flow.model.Draft()
		_ = d.processEdit.SetText(draft.ProcessName)
		d.manualOnly.SetChecked(d.flow.manualOnly && strings.TrimSpace(draft.ProcessName) == "")
		d.updateMonitorDetails()
	})

	if refreshErr != nil {
		d.setStatus("重新整理失敗：" + refreshErr.Error())
	} else if d.flow.model.LegacyPrefilled() {
		d.setStatus("偵測到既有設定，確認後儲存")
	} else {
		d.setStatus("")
	}
	d.applyGates()
}

func (d *settingsDialog) applyGates() {
	gates := d.flow.Gates()
	d.modeGroup.SetEnabled(gates.Mode)
	d.processGroup.SetEnabled(gates.Process)
	d.saveButton.SetEnabled(gates.Save)
	// A disabled section is inert: clicks, typing and list selections all do nothing,
	// and Walk gives no sign of why. Each section therefore states its own
	// precondition, so the answer is beside the controls that are refusing rather
	// than only in the status line at the bottom.
	if tip := lockedSectionTip(gates); tip != "" {
		_ = d.processTip.SetText(tip)
		_ = d.processTip.SetToolTipText(tip)
	}
	// The label tracks the gate rather than being written once: a reason that
	// outlives the condition it described is worse than no reason, because the user
	// fixes what it names and watches nothing change.
	if !gates.Save {
		d.setStatus(gates.Reason)
	} else if d.statusLabel.Text() == gates.Reason || isGateReason(d.statusLabel.Text()) {
		d.setStatus("")
	}
}

// isGateReason reports whether the status currently shows a gate's explanation, so
// clearing it cannot swallow a message that came from somewhere else -- a failed
// save, say, which the user still needs to read.
func isGateReason(text string) bool {
	if text == "" {
		return false
	}
	for _, reason := range gateReasons {
		if text == reason {
			return true
		}
	}
	return false
}

func (d *settingsDialog) onRefresh() {
	err := d.flow.model.Refresh()
	d.flow.syncInitialProcessChoice()
	d.rebuild(err)
}

func (d *settingsDialog) onMonitorSelected() {
	if d.flow.rebuilding {
		return
	}
	if err := d.flow.SelectMonitor(d.monitorView.CurrentIndex()); err != nil {
		d.setStatus(err.Error())
		return
	}
	d.rebuild(nil)
}

func (d *settingsDialog) onAspectChanged() {
	if d.flow.rebuilding {
		return
	}
	index := d.aspectBox.CurrentIndex()
	filters := aspectFilters()
	if index < 0 || index >= len(filters) {
		return
	}
	d.aspect = filters[index]
	d.flow.withRebuild(func() {
		d.modeTable.rows = d.flow.model.ModeRows(d.aspect)
		d.modeTable.PublishRowsReset()
		d.selectDraftModeRow()
		d.rebuildRefreshRates()
	})
	d.applyGates()
}

func (d *settingsDialog) onModeSelected() {
	if d.flow.rebuilding {
		return
	}
	index := d.modeView.CurrentIndex()
	if index < 0 || index >= len(d.modeTable.rows) {
		return
	}
	row := d.modeTable.rows[index]
	if err := d.flow.SelectResolution(row.Width, row.Height); err != nil {
		d.setStatus(err.Error())
		return
	}
	d.flow.withRebuild(d.rebuildRefreshRates)
	d.updateModeDetails(row)
	d.applyGates()
}

func (d *settingsDialog) onRefreshChanged() {
	if d.flow.rebuilding {
		return
	}
	draft := d.flow.model.Draft()
	index := d.refreshBox.CurrentIndex()
	row, ok := selectedModeRow(d.modeTable.rows, draft.GameMode.Width, draft.GameMode.Height)
	if !ok || index < 0 || index >= len(row.RefreshRates) {
		return
	}
	mode := domain.Mode{Width: row.Width, Height: row.Height, RefreshHz: row.RefreshRates[index], BitsPerPixel: 32}
	if err := d.flow.SelectMode(mode); err != nil {
		d.setStatus(err.Error())
		return
	}
	d.applyGates()
}

func (d *settingsDialog) onProcessSearch() {
	if d.flow.rebuilding {
		return
	}
	d.flow.withRebuild(d.rebuildProcesses)
}

func (d *settingsDialog) onProcessSelected() {
	if d.flow.rebuilding {
		return
	}
	index := d.processList.CurrentIndex()
	if index < 0 || index >= len(d.processRows) {
		return
	}
	name := d.processRows[index]
	d.flow.SetProcessName(name)
	d.flow.withRebuild(func() {
		d.manualOnly.SetChecked(false)
		_ = d.processEdit.SetText(name)
	})
	d.applyGates()
}

func (d *settingsDialog) onProcessTextChanged() {
	if d.flow.rebuilding || d.manualOnly.Checked() {
		return
	}
	d.flow.SetProcessName(d.processEdit.Text())
	d.applyGates()
}

func (d *settingsDialog) onManualOnlyChanged() {
	if d.flow.rebuilding {
		return
	}
	if d.manualOnly.Checked() {
		d.flow.UseManualOnly()
		d.flow.withRebuild(func() { _ = d.processEdit.SetText("") })
	} else {
		d.flow.clearProcessChoice()
	}
	d.applyGates()
}

func (d *settingsDialog) onSave() {
	if err := d.flow.Save(); err != nil {
		d.setStatus("儲存失敗：" + err.Error())
		d.applyGates()
		return
	}
	d.dialog.Accept()
}

func (d *settingsDialog) onCancel() {
	d.flow.Cancel()
	d.dialog.Cancel()
}

func (d *settingsDialog) rebuildProcesses() {
	d.processRows = d.flow.model.ProcessNames(d.processSearch.Text())
	_ = d.processList.SetModel(d.processRows)
	_ = d.processList.SetCurrentIndex(indexOfStringFold(d.processRows, d.flow.model.Draft().ProcessName))
	if err := d.flow.model.ProcessError(); err != nil {
		_ = d.processTip.SetText("無法讀取目前程序清單：" + err.Error() + "；仍可手動輸入")
	} else {
		_ = d.processTip.SetText("可從清單選擇、手動輸入，或明確選擇只用手動切換")
	}
}

func (d *settingsDialog) selectDraftModeRow() {
	draft := d.flow.model.Draft()
	index := -1
	for i, row := range d.modeTable.rows {
		if row.Width == draft.GameMode.Width && row.Height == draft.GameMode.Height {
			index = i
			break
		}
	}
	_ = d.modeView.SetCurrentIndex(index)
	if index >= 0 {
		d.updateModeDetails(d.modeTable.rows[index])
	} else {
		_ = d.modeTip.SetText("請選擇顯示模式")
	}
}

func (d *settingsDialog) rebuildRefreshRates() {
	draft := d.flow.model.Draft()
	row, ok := selectedModeRow(d.modeTable.rows, draft.GameMode.Width, draft.GameMode.Height)
	if !ok {
		_ = d.refreshBox.SetModel([]string{})
		_ = d.refreshLabel.SetText("刷新率：")
		return
	}
	labels := make([]string, len(row.RefreshRates))
	selected := -1
	for i, refresh := range row.RefreshRates {
		labels[i] = strconv.FormatUint(uint64(refresh), 10) + " Hz"
		if refresh == draft.GameMode.RefreshHz {
			selected = i
		}
	}
	_ = d.refreshBox.SetModel(labels)
	_ = d.refreshBox.SetCurrentIndex(selected)
	_ = d.refreshLabel.SetText(fmt.Sprintf("刷新率（%d 種）：", len(labels)))
}

func (d *settingsDialog) updateMonitorDetails() {
	index := d.flow.model.selectedMonitor
	rows := d.flow.model.MonitorRows()
	if index < 0 || index >= len(rows) {
		_ = d.monitorTip.SetText("請選擇一台顯示器")
		_ = d.monitorView.SetToolTipText("")
		return
	}
	row := rows[index]
	tip := monitorIdentityDetails(row)
	_ = d.monitorTip.SetText(tip)
	_ = d.monitorView.SetToolTipText(tip)
}

func (d *settingsDialog) updateModeDetails(row modeRow) {
	text := modeNotes(row, d.flow.model.scaling)
	if row.FullScreenScalingReminder {
		// The one thing the tool cannot do for the user belongs where the user is
		// making the choice it affects, not only in the main window.
		text += "\n" + scalingOverrideNote
	}
	_ = d.modeTip.SetText(text)
	_ = d.modeTip.SetToolTipText(text)
}

func (d *settingsDialog) setStatus(text string) {
	_ = d.statusLabel.SetText(text)
	_ = d.statusLabel.SetToolTipText(text)
}

type monitorTableModel struct {
	walk.TableModelBase
	rows []monitorRow
}

func (m *monitorTableModel) RowCount() int { return len(m.rows) }
func (m *monitorTableModel) Value(row, column int) interface{} {
	item := m.rows[row]
	switch column {
	case 0:
		return monitorRowLabel(item)
	case 1:
		return item.Target.DeviceName
	case 2:
		if item.HasCurrentMode {
			return domain.ModeLabel(item.CurrentMode)
		}
		return "未知"
	case 3:
		if item.Primary {
			return "是"
		}
		return "否"
	case 4:
		return item.Reason
	default:
		return ""
	}
}

type modeTableModel struct {
	walk.TableModelBase
	rows    []modeRow
	scaling app.ScalingSnapshot
}

func (m *modeTableModel) RowCount() int { return len(m.rows) }
func (m *modeTableModel) Value(row, column int) interface{} {
	item := m.rows[row]
	switch column {
	case 0:
		return fmt.Sprintf("%d × %d", item.Width, item.Height)
	case 1:
		return item.Aspect
	case 2:
		return fmt.Sprintf("%d Hz", item.HighestRefresh)
	case 3:
		return modeNotes(item, m.scaling)
	default:
		return ""
	}
}

func aspectFilters() []aspectFilter {
	return []aspectFilter{aspectAll, aspectFourByThree, aspectSixteenByNine, aspectSixteenByTen, aspectOther}
}

func aspectFilterNames() []string {
	filters := aspectFilters()
	names := make([]string, len(filters))
	for i, filter := range filters {
		names[i] = string(filter)
	}
	return names
}

func aspectFilterIndex(want aspectFilter) int {
	for index, filter := range aspectFilters() {
		if filter == want {
			return index
		}
	}
	return 0
}

func selectedModeRow(rows []modeRow, width, height uint32) (modeRow, bool) {
	for _, row := range rows {
		if row.Width == width && row.Height == height {
			return row, true
		}
	}
	return modeRow{}, false
}

func monitorRowLabel(row monitorRow) string {
	switch {
	case strings.TrimSpace(row.Target.Identity.Label) != "":
		return strings.TrimSpace(row.Target.Identity.Label)
	case row.Target.Identity.InstancePath != "":
		return row.Target.Identity.InstancePath
	default:
		return row.Target.Identity.HardwareID
	}
}

func monitorIdentityDetails(row monitorRow) string {
	details := []string{monitorRowLabel(row), row.Target.DeviceName}
	if row.Target.Identity.InstancePath != "" {
		details = append(details, "裝置介面路徑："+row.Target.Identity.InstancePath)
	}
	if row.Target.Identity.HardwareID != "" {
		details = append(details, "硬體 ID："+row.Target.Identity.HardwareID)
	}
	if row.Reason != "" {
		details = append(details, row.Reason)
	}
	return strings.Join(details, "\n")
}

func modeNotes(row modeRow, view app.ScalingSnapshot) string {
	var notes []string
	if row.Current {
		notes = append(notes, "目前模式")
	}
	if row.Native {
		notes = append(notes, "原生模式")
	}
	if row.FullScreenScalingReminder {
		notes = append(notes, modeScalingNote(view))
	}
	if len(notes) == 0 {
		return "—"
	}
	return strings.Join(notes, "；")
}

// modeScalingNote is the profile design's static reminder wherever the scaling value
// could not be read, and names the read-back setting wherever it could. It never
// invents a value or infers whether black bars are visible from that setting.
func modeScalingNote(view app.ScalingSnapshot) string {
	if !view.Known {
		return "非原生比例，請確認已使用全螢幕縮放"
	}
	note := "非原生比例，目前 GPU 縮放：" + app.ScalingLabel(view.Effective)
	if view.Effective.Mode == app.ScalingFullScreenByGPU().Mode {
		return note
	}
	return note + "，畫面可能有黑邊"
}

func indexOfStringFold(values []string, want string) int {
	for index, value := range values {
		if strings.EqualFold(value, want) {
			return index
		}
	}
	return -1
}

// lockedSectionTip is the sentence the process section shows while it is disabled,
// or empty when it is usable and its own tip should stand.
//
// The section is disabled by SetEnabled on its container, which silently makes every
// control inside it ignore the user. Saying nothing there is what made a selection
// that never registered look like a bug in the list rather than an unfinished step
// above it.
func lockedSectionTip(gates settingsGates) string {
	switch {
	case !gates.Mode:
		return "請先在上方選擇一台顯示器"
	case !gates.Process:
		return "請先在上方選擇顯示模式"
	default:
		return ""
	}
}
