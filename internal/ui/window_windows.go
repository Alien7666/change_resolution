//go:build windows

// Package ui renders an app.Session through a native Walk window and a
// notification-area icon. Every Walk object is touched only on the UI thread;
// blocking session work runs in goroutines and is marshalled back with
// (*walk.MainWindow).Synchronize.
package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

const (
	windowTitle = "VALORANT 4:3 顯示工具"
	monitorName = "Mi Monitor (XMI27B2)"

	toggleText  = "使用 4:3（1920×1440 @ 180 Hz）"
	hideText    = "隱藏至系統匣"
	restoreText = "恢復 2K"
	refreshText = "重新整理"

	trayShowText    = "顯示主視窗"
	trayEnableText  = "使用 4:3"
	trayRestoreText = "恢復原始解析度"
	trayRefreshText = "重新整理狀態"
	trayExitText    = "結束"

	enableOperation      = "啟用 4:3"
	restoreOperation     = "恢復顯示模式"
	refreshOperation     = "重新整理顯示狀態"
	autoRestoreOperation = "自動恢復顯示模式"
	shutdownOperation    = "結束前恢復顯示模式"

	windowWidth  = 420
	windowHeight = 240
)

// window owns every Walk object. All of its fields are read and written on the
// Walk UI thread only.
type window struct {
	session *app.Session

	mw   *walk.MainWindow
	tray *walk.NotifyIcon

	targetLabel   *walk.Label
	modeLabel     *walk.Label
	toggle        *walk.CheckBox
	statusLabel   *walk.TextLabel
	refreshButton *walk.PushButton
	hideButton    *walk.PushButton
	restoreButton *walk.PushButton

	showAction    *walk.Action
	enableAction  *walk.Action
	restoreAction *walk.Action
	refreshAction *walk.Action
	exitAction    *walk.Action

	busy              bool
	suppressToggle    bool
	unavailableReason string

	// reportedAutoRestoreFailures is the highest Snapshot.AutoRestoreFailures this
	// window has already announced.
	reportedAutoRestoreFailures uint64
}

// Run builds the main window plus the notification-area icon and blocks on the
// Walk message loop until the tray exit action ends the application.
func Run(session *app.Session) error {
	if session == nil {
		return errors.New("ui: session must not be nil")
	}

	w := &window{session: session}
	if err := w.buildMainWindow(); err != nil {
		return err
	}
	if err := w.buildTray(); err != nil {
		return err
	}
	defer func() { _ = w.tray.Dispose() }()

	session.SetOnChange(func(snapshot app.Snapshot) {
		w.mw.Synchronize(func() { w.render(snapshot) })
	})

	w.render(session.Snapshot())

	// Startup is read-only. Refresh talks to Win32, so it runs off the UI thread
	// and only reports what it found; it never changes a display mode.
	go func() {
		// A failed startup probe needs no dialog: it is rendered into the status
		// label, and the refresh command stays live so the user can read again.
		snapshot, _ := session.Refresh()
		w.mw.Synchronize(func() { w.render(snapshot) })
	}()

	// walk creates the form hidden (WS_OVERLAPPEDWINDOW has no WS_VISIBLE), so the
	// main window is shown explicitly before the message loop starts.
	w.mw.Show()
	w.mw.Run()
	return nil
}

func (w *window) buildMainWindow() error {
	err := dec.MainWindow{
		AssignTo: &w.mw,
		Title:    windowTitle,
		MinSize:  dec.Size{Width: windowWidth, Height: windowHeight},
		MaxSize:  dec.Size{Width: windowWidth, Height: windowHeight},
		Size:     dec.Size{Width: windowWidth, Height: windowHeight},
		Layout: dec.VBox{
			Margins: dec.Margins{Left: 12, Top: 12, Right: 12, Bottom: 12},
			Spacing: 8,
		},
		OnSizeChanged: w.onSizeChanged,
		Children: []dec.Widget{
			dec.Label{AssignTo: &w.targetLabel, Text: "目標螢幕：" + monitorName},
			dec.Label{AssignTo: &w.modeLabel, Text: "目前模式：讀取中"},
			dec.CheckBox{
				AssignTo:         &w.toggle,
				Text:             toggleText,
				OnCheckedChanged: w.onToggled,
			},
			dec.TextLabel{
				AssignTo: &w.statusLabel,
				Text:     "狀態：啟動中",
				MinSize:  dec.Size{Height: 48},
			},
			dec.VSpacer{},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.HSpacer{},
					dec.PushButton{AssignTo: &w.refreshButton, Text: refreshText, OnClicked: w.onRefresh},
					dec.PushButton{AssignTo: &w.hideButton, Text: hideText, OnClicked: w.onHide},
					dec.PushButton{AssignTo: &w.restoreButton, Text: restoreText, OnClicked: w.onRestore},
				},
			},
		},
	}.Create()
	if err != nil {
		return fmt.Errorf("create main window: %w", err)
	}

	if err := w.mw.SetIcon(walk.IconApplication()); err != nil {
		return fmt.Errorf("set window icon: %w", err)
	}
	w.freezeSize()

	// Closing the main window only hides it; the tray icon keeps the session and
	// the message loop alive.
	w.mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		*canceled = true
		w.hideToTray()
	})
	return nil
}

// freezeSize drops the resize and maximise affordances so the window keeps the
// fixed 420x240 layout.
func (w *window) freezeSize() {
	hwnd := w.mw.Handle()
	style := win.GetWindowLong(hwnd, win.GWL_STYLE)
	win.SetWindowLong(hwnd, win.GWL_STYLE, style&^(win.WS_THICKFRAME|win.WS_MAXIMIZEBOX))
	win.SetWindowPos(hwnd, 0, 0, 0, 0, 0,
		win.SWP_NOMOVE|win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE|win.SWP_FRAMECHANGED)
}

func (w *window) buildTray() error {
	tray, err := walk.NewNotifyIcon(w.mw)
	if err != nil {
		return fmt.Errorf("create notification icon: %w", err)
	}
	w.tray = tray

	if err := tray.SetIcon(walk.IconApplication()); err != nil {
		return fmt.Errorf("set notification icon: %w", err)
	}
	if err := tray.SetToolTip(windowTitle); err != nil {
		return fmt.Errorf("set notification tooltip: %w", err)
	}

	if w.showAction, err = newAction(trayShowText, w.onShow); err != nil {
		return err
	}
	if w.enableAction, err = newAction(trayEnableText, w.onEnable); err != nil {
		return err
	}
	if w.restoreAction, err = newAction(trayRestoreText, w.onRestore); err != nil {
		return err
	}
	if w.refreshAction, err = newAction(trayRefreshText, w.onRefresh); err != nil {
		return err
	}
	if w.exitAction, err = newAction(trayExitText, w.onExit); err != nil {
		return err
	}

	actions := tray.ContextMenu().Actions()
	menu := []*walk.Action{
		w.showAction,
		w.enableAction,
		w.restoreAction,
		w.refreshAction,
		walk.NewSeparatorAction(),
		w.exitAction,
	}
	for _, action := range menu {
		if err := actions.Add(action); err != nil {
			return fmt.Errorf("add notification menu action: %w", err)
		}
	}

	tray.MouseDown().Attach(func(_, _ int, button walk.MouseButton) {
		if button == walk.LeftButton {
			w.onShow()
		}
	})

	if err := tray.SetVisible(true); err != nil {
		return fmt.Errorf("show notification icon: %w", err)
	}
	return nil
}

func newAction(text string, handler walk.EventHandler) (*walk.Action, error) {
	action := walk.NewAction()
	if err := action.SetText(text); err != nil {
		return nil, fmt.Errorf("set action text %q: %w", text, err)
	}
	action.Triggered().Attach(handler)
	return action, nil
}

func (w *window) onToggled() {
	if w.suppressToggle {
		return
	}
	if w.toggle.Checked() {
		w.runOperation(enableOperation, w.session.Enable)
		return
	}
	w.runOperation(restoreOperation, w.session.Disable)
}

func (w *window) onEnable()  { w.runOperation(enableOperation, w.session.Enable) }
func (w *window) onRestore() { w.runOperation(restoreOperation, w.session.Disable) }
func (w *window) onRefresh() { w.runOperation(refreshOperation, w.refresh) }
func (w *window) onHide()    { w.hideToTray() }
func (w *window) onShow()    { w.showMainWindow() }

// refresh re-reads the display state through the session. It is the way out of
// the unavailable latch: a monitor that was asleep or on another input when the
// tool started leaves every mutating control disabled, and only a fresh read can
// tell the window that the monitor came back.
func (w *window) refresh() error {
	_, err := w.session.Refresh()
	return err
}

// onExit restores any mode this run applied before the process ends. A failed
// restore keeps the application alive so the user can retry instead of silently
// abandoning the display in 4:3.
func (w *window) onExit() {
	if w.busy {
		return
	}
	w.busy = true
	w.render(w.session.Snapshot())

	go func() {
		err := w.session.Shutdown()
		snapshot := w.session.Snapshot()
		w.mw.Synchronize(func() {
			w.busy = false
			w.render(snapshot)
			if err != nil {
				w.reportError(shutdownOperation, err)
				return
			}
			_ = w.tray.Dispose()
			walk.App().Exit(0)
		})
	}()
}

// runOperation disables the mutating controls, performs one session operation off
// the UI thread, then re-renders and reports failures on the UI thread.
func (w *window) runOperation(operation string, action func() error) {
	if w.busy {
		return
	}
	w.busy = true
	w.render(w.session.Snapshot())

	go func() {
		err := action()
		snapshot := w.session.Snapshot()
		w.mw.Synchronize(func() {
			w.busy = false
			w.render(snapshot)
			if err != nil && !errors.Is(err, app.ErrClosed) {
				w.reportError(operation, err)
			}
		})
	}()
}

func (w *window) reportError(operation string, err error) {
	message := operation + "失敗：\n" + err.Error()
	if w.mw.Visible() {
		walk.MsgBox(w.mw, windowTitle, message, walk.MsgBoxIconError|walk.MsgBoxSetForeground)
		return
	}
	// Hidden in the notification area: a balloon keeps a failed restore visible.
	_ = w.tray.ShowError(windowTitle, message)
}

func (w *window) hideToTray() { w.mw.Hide() }

func (w *window) showMainWindow() {
	hwnd := w.mw.Handle()
	if win.IsIconic(hwnd) {
		win.ShowWindow(hwnd, win.SW_RESTORE)
	}
	w.mw.Show()
	_ = w.mw.Activate()
	win.SetForegroundWindow(hwnd)
}

// onSizeChanged turns minimising into hiding so the tool lives in the
// notification area rather than the taskbar.
func (w *window) onSizeChanged() {
	if w.mw == nil {
		return
	}
	if win.IsIconic(w.mw.Handle()) && w.mw.Visible() {
		w.mw.Hide()
	}
}

func (w *window) render(snapshot app.Snapshot) {
	w.updateAvailability(snapshot)

	_ = w.targetLabel.SetText(targetText(snapshot))
	_ = w.modeLabel.SetText(modeText(snapshot))
	_ = w.statusLabel.SetText(w.statusText(snapshot))

	// While an operation runs the checkbox reflects the user intent, not the
	// transient session state.
	if !w.busy {
		w.setToggleChecked(snapshot.AtGameMode)
	}
	w.applyEnabled(snapshot)

	if err := w.takeAutoRestoreFailure(snapshot); err != nil {
		w.reportError(autoRestoreOperation, err)
	}
}

// takeAutoRestoreFailure returns the error of an automatic restore this window has
// not announced yet. runOperation already reports every failure the user asked
// for; the watcher has no such path, so the session counts its own failures and
// this window reports each count once. Re-rendering the same failed state, which
// every later poll does, stays silent.
func (w *window) takeAutoRestoreFailure(snapshot app.Snapshot) error {
	if snapshot.AutoRestoreFailures <= w.reportedAutoRestoreFailures {
		return nil
	}
	w.reportedAutoRestoreFailures = snapshot.AutoRestoreFailures
	if snapshot.Err == nil {
		return errors.New("恢復原始顯示模式時發生未知錯誤")
	}
	return snapshot.Err
}

// updateAvailability latches the conditions that must disable the 4:3 toggle. Three
// of them are ways the target monitor cannot be resolved -- it is not attached, the
// configured identity hits several monitors, or the display device it resolves to is
// mirrored onto another screen -- and the fourth is a target mode the driver refuses.
// A clean read clears the latch.
//
// The two resolution refusals are kept apart because the way out of them differs: a
// missing monitor comes back on its own when it wakes up or is switched back to this
// input, so re-reading is the answer, while ambiguity and mirroring need the user to
// change something. Telling them the same thing for all three would send them to
// re-read a state that re-reading cannot fix.
func (w *window) updateAvailability(snapshot app.Snapshot) {
	switch {
	case errors.Is(snapshot.Err, display.ErrTargetNotFound):
		w.unavailableReason = "找不到 " + monitorName + "，已停用 4:3 切換。"
	case errors.Is(snapshot.Err, display.ErrTargetAmbiguous):
		// The candidates live in the error text because they are the only thing that
		// makes this actionable: the user has to know which screens were hit before
		// they can pick one in the settings.
		w.unavailableReason = "設定的顯示器同時對應到多台，已停用顯示模式切換，請重新選擇顯示器。\n" +
			snapshot.Err.Error()
	case errors.Is(snapshot.Err, display.ErrTargetMirrored):
		w.unavailableReason = "設定的顯示器與另一台共用同一個顯示裝置（複製／鏡射），" +
			"本工具無法只變更其中一台，已停用顯示模式切換。\n" + snapshot.Err.Error()
	case errors.Is(snapshot.Err, display.ErrModeNotSupported):
		w.unavailableReason = "顯示器不支援 1920×1440 @ 180 Hz，已停用 4:3 切換。"
	case snapshot.Err == nil && snapshot.Target.DeviceName != "":
		w.unavailableReason = ""
	}
}

// controls says which commands accept input. It is one value so the policy can be
// decided without touching Walk and asserted in a test.
type controls struct {
	toggle  bool
	restore bool
	enable  bool
	refresh bool
	hide    bool
	show    bool
	exit    bool
}

// availableControls keeps the read-only command live while the target monitor is
// unavailable: re-reading is the only way out of that latch. The 4:3 commands stay
// disabled with the reason on screen.
func (w *window) availableControls(snapshot app.Snapshot) controls {
	interactive := w.unavailableReason == "" && !w.busy
	return controls{
		toggle:  interactive,
		restore: interactive,
		enable:  interactive && !snapshot.AtGameMode,
		refresh: !w.busy,
		hide:    true,
		show:    true,
		exit:    !w.busy,
	}
}

func (w *window) applyEnabled(snapshot app.Snapshot) {
	available := w.availableControls(snapshot)

	w.toggle.SetEnabled(available.toggle)
	w.restoreButton.SetEnabled(available.restore)
	w.refreshButton.SetEnabled(available.refresh)
	w.hideButton.SetEnabled(available.hide)

	_ = w.showAction.SetEnabled(available.show)
	_ = w.enableAction.SetEnabled(available.enable)
	_ = w.restoreAction.SetEnabled(available.restore)
	_ = w.refreshAction.SetEnabled(available.refresh)
	_ = w.exitAction.SetEnabled(available.exit)
}

func (w *window) setToggleChecked(checked bool) {
	if w.toggle.Checked() == checked {
		return
	}
	w.suppressToggle = true
	w.toggle.SetChecked(checked)
	w.suppressToggle = false
}

func (w *window) statusText(snapshot app.Snapshot) string {
	var builder strings.Builder
	builder.WriteString("狀態：")
	if snapshot.Message != "" {
		builder.WriteString(snapshot.Message)
	} else {
		builder.WriteString(stateText(snapshot.State))
	}
	if w.unavailableReason != "" {
		builder.WriteString("\n")
		builder.WriteString(w.unavailableReason)
	}
	return builder.String()
}

func targetText(snapshot app.Snapshot) string {
	if snapshot.Target.DeviceName == "" {
		return "目標螢幕：" + monitorName + "（尚未找到）"
	}
	return "目標螢幕：" + monitorName + " → " + snapshot.Target.DeviceName
}

func modeText(snapshot app.Snapshot) string {
	mode := snapshot.CurrentMode
	if mode.Width == 0 || mode.Height == 0 {
		return "目前模式：未知"
	}
	return fmt.Sprintf("目前模式：%d × %d @ %d Hz（%d bpp）",
		mode.Width, mode.Height, mode.RefreshHz, mode.BitsPerPixel)
}

func stateText(state app.State) string {
	switch state {
	case app.StateNative:
		return "尚未啟用 4:3"
	case app.StateApplying:
		return "正在套用 4:3"
	case app.StateWaitingForGame:
		return "4:3 已啟用，等待遊戲"
	case app.StateGameRunning:
		return "遊戲執行中"
	case app.StateRestorePending:
		return "遊戲已關閉，等待恢復顯示模式"
	case app.StateRestoring:
		return "正在恢復顯示模式"
	case app.StateError:
		return "發生錯誤"
	default:
		return string(state)
	}
}
