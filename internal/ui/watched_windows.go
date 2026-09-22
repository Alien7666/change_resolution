package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"

	"github.com/Alien7666/change_resolution/internal/config"
)

const (
	watchedChangeText  = "變更要監看的程式…"
	watchedDialogTitle = "變更要監看的程式"
	watchedManualText  = "不監看任何程式，只手動切換"
	watchedSearchText  = "搜尋"
	watchedBrowseText  = "選擇執行檔…"
	watchedSaveText    = "套用"
	watchedCancelText  = "取消"

	// watchedLockedReason is why the button is off. It names the state rather than the
	// fix, because the fix -- resolve the pending recovery -- already has its own
	// sentence on the restore button and repeating it here would be two instructions
	// for one action.
	watchedLockedReason = "顯示配置仍待恢復，請先恢復原始顯示模式。"
)

// watchedList is the pure half of the dialog: the process names that were read, the
// error if they could not be, and the filtering the search box does. It exists apart
// from the Walk widgets for the same reason the settings model does -- the wiring
// cannot be tested without a message loop, but the answers can.
type watchedList struct {
	names []string
	err   error
}

func (l watchedList) matching(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	var names []string
	for _, name := range l.names {
		if strings.Contains(strings.ToLower(name), query) {
			names = append(names, name)
		}
	}
	return names
}

// watchedResult is what pressing 套用 means, worked out from the two controls that can
// disagree. The checkbox wins: a user who ticked "watch nothing" has said so in the
// plainest way the dialog offers, and a name left in the edit box behind it is a
// leftover rather than a second opinion.
func watchedResult(typed string, manualOnly bool) (string, error) {
	if manualOnly {
		return "", nil
	}
	name := strings.TrimSpace(typed)
	if name == "" {
		return "", fmt.Errorf("請選擇或輸入要監看的程式，或勾選「%s」", watchedManualText)
	}
	if err := config.ValidateProcessName(name); err != nil {
		return "", err
	}
	return name, nil
}

// watchedDialog is the entry point the settings dialog cannot be. Settings is disabled
// for as long as a mode is applied, and "I am playing something else today" is asked
// precisely then, so the one field that is safe to change while managed gets its own
// window.
type watchedDialog struct {
	dialog     *walk.Dialog
	search     *walk.LineEdit
	list       *walk.ListBox
	edit       *walk.LineEdit
	manualOnly *walk.CheckBox
	tip        *walk.TextLabel
	status     *walk.TextLabel

	owner   walk.Form
	source  watchedList
	rows    []string
	current string

	// rebuilding suppresses the change handlers while the dialog writes to its own
	// controls. Without it, setting the edit box from the list selection re-enters the
	// edit handler and fights the selection that caused it.
	rebuilding bool

	result   string
	accepted bool
}

func runWatchedDialog(owner walk.Form, source watchedList, current string) (string, bool, error) {
	d := &watchedDialog{owner: owner, source: source, current: strings.TrimSpace(current)}
	if err := d.build(); err != nil {
		return "", false, err
	}
	d.dialog.Run()
	return d.result, d.accepted, nil
}

func (d *watchedDialog) build() error {
	var save, cancel *walk.PushButton
	err := dec.Dialog{
		AssignTo: &d.dialog,
		Title:    watchedDialogTitle,
		MinSize:  dec.Size{Width: 420, Height: 380},
		Layout: dec.VBox{
			Margins: dec.Margins{Left: 12, Top: 12, Right: 12, Bottom: 12},
			Spacing: 8,
		},
		Children: []dec.Widget{
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.LineEdit{
						AssignTo: &d.search,
						// Enter in the search box searches. It used to reach the
						// dialog's default button and save instead, which is why this
						// dialog declares no default button at all.
						OnEditingFinished: d.onSearch,
					},
					dec.PushButton{Text: watchedSearchText, OnClicked: d.onSearch},
				},
			},
			dec.ListBox{
				AssignTo: &d.list,
				Model:    []string{},
				MinSize:  dec.Size{Height: 140},
				// SelChanged alone does not fire when a search narrows the list to the
				// row that was already current, so the click and the double-click are
				// wired as well.
				OnCurrentIndexChanged: d.onSelected,
				OnItemActivated:       d.onSelected,
				OnMouseUp:             func(int, int, walk.MouseButton) { d.onSelected() },
			},
			dec.TextLabel{AssignTo: &d.tip, Text: "", MinSize: dec.Size{Height: 32}},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.LineEdit{AssignTo: &d.edit, OnTextChanged: d.onTyped},
					dec.PushButton{Text: watchedBrowseText, OnClicked: d.onBrowse},
				},
			},
			dec.CheckBox{
				AssignTo:         &d.manualOnly,
				Text:             watchedManualText,
				OnCheckedChanged: d.onManualOnly,
			},
			dec.TextLabel{AssignTo: &d.status, Text: "", MinSize: dec.Size{Height: 32}},
			dec.VSpacer{},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.HSpacer{},
					dec.PushButton{AssignTo: &save, Text: watchedSaveText, OnClicked: d.onSave},
					dec.PushButton{AssignTo: &cancel, Text: watchedCancelText, OnClicked: d.dialog.Cancel},
				},
			},
		},
	}.Create(d.owner)
	if err != nil {
		return fmt.Errorf("create watched-process dialog: %w", err)
	}
	d.dialog.SetCancelButton(cancel)

	d.withRebuild(func() {
		_ = d.edit.SetText(d.current)
		d.manualOnly.SetChecked(d.current == "")
	})
	d.rebuild()
	return nil
}

func (d *watchedDialog) withRebuild(change func()) {
	was := d.rebuilding
	d.rebuilding = true
	defer func() { d.rebuilding = was }()
	change()
}

func (d *watchedDialog) rebuild() {
	d.rows = d.source.matching(d.search.Text())
	d.withRebuild(func() {
		_ = d.list.SetModel(d.rows)
		_ = d.list.SetCurrentIndex(indexOfStringFold(d.rows, d.edit.Text()))
	})
	d.tip.SetText(processTipText(d.search.Text(), len(d.rows), len(d.source.names), d.source.err))
	d.status.SetText(notRunningNote(d.edit.Text(), d.source.names, d.source.err))
}

func (d *watchedDialog) onSearch() {
	if d.rebuilding {
		return
	}
	d.rebuild()
}

func (d *watchedDialog) onSelected() {
	if d.rebuilding {
		return
	}
	index := d.list.CurrentIndex()
	if index < 0 || index >= len(d.rows) {
		return
	}
	name := d.rows[index]
	d.withRebuild(func() {
		_ = d.edit.SetText(name)
		d.manualOnly.SetChecked(false)
	})
	d.status.SetText(notRunningNote(name, d.source.names, d.source.err))
}

func (d *watchedDialog) onTyped() {
	if d.rebuilding {
		return
	}
	if strings.TrimSpace(d.edit.Text()) != "" {
		d.withRebuild(func() { d.manualOnly.SetChecked(false) })
	}
	d.status.SetText(notRunningNote(d.edit.Text(), d.source.names, d.source.err))
}

func (d *watchedDialog) onManualOnly() {
	if d.rebuilding {
		return
	}
	if d.manualOnly.Checked() {
		d.status.SetText("")
		return
	}
	d.status.SetText(notRunningNote(d.edit.Text(), d.source.names, d.source.err))
}

func (d *watchedDialog) onBrowse() {
	dlg := &walk.FileDialog{
		Title:  "選擇要監看的遊戲執行檔",
		Filter: "程式 (*.exe)|*.exe|所有檔案 (*.*)|*.*",
	}
	accepted, err := dlg.ShowOpen(d.dialog)
	if err != nil {
		d.status.SetText("開啟檔案選擇器失敗：" + err.Error())
		return
	}
	if !accepted || strings.TrimSpace(dlg.FilePath) == "" {
		return
	}
	// Toolhelp reports bare image names, so only the base name can ever match.
	name := filepath.Base(dlg.FilePath)
	d.withRebuild(func() {
		_ = d.edit.SetText(name)
		d.manualOnly.SetChecked(false)
	})
	d.rebuild()
}

func (d *watchedDialog) onSave() {
	name, err := watchedResult(d.edit.Text(), d.manualOnly.Checked())
	if err != nil {
		d.status.SetText(err.Error())
		return
	}
	d.result, d.accepted = name, true
	d.dialog.Accept()
}
