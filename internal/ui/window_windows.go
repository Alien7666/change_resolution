//go:build windows

// Package ui renders an app.Provider through a native Walk window and a
// notification-area icon. Every Walk object is touched only on the UI thread;
// blocking session work runs in goroutines and is marshalled back with
// (*walk.MainWindow).Synchronize.
//
// Nothing in this package knows what the user configured. Every string a person
// reads is a function of the app.Snapshot it is rendering -- the monitor's name, the
// mode a toggle applies, the mode a restore returns to, the process that is watched
// and the delay it is watched with all arrive as data. The constants that used to
// spell out one machine's hardware are gone, and the tests fail if any of them comes
// back, because a window that describes a profile the user did not save is worse than
// a window that says nothing.
package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	processcheck "github.com/Alien7666/change_resolution/internal/process"
	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

const (
	// windowTitle is the product's name, which is why it survived the cull of
	// hardware constants. It names neither a game nor an aspect ratio: the tool
	// switches whatever mode its owner configured, and a title claiming otherwise
	// would be wrong for everyone but its first user. It matches the executable,
	// the repository and the release assets, so the three cannot drift apart.
	windowTitle = "ResolutionTray"

	hideText    = "隱藏至系統匣"
	refreshText = "重新整理"
	reloadText  = "重新讀取"

	openConfigFolderText = "開啟設定檔所在資料夾"
	resetConfigText      = "重新設定"
	settingsText         = "設定…"
	settingsManagedText  = "請先恢復原始解析度再變更設定"
	settingsRecoveryText = "請先恢復寫入前的顯示配置再變更設定"
	settingsScalingText  = "請先還原 GPU 縮放設定再變更設定"

	// The GPU-scaling row. Every literal here names an operation or a decision this
	// product made; none of them names a monitor, a mode or an aspect, because all
	// three arrive in the snapshot.
	scalingApplyText      = "設為全螢幕縮放（GPU）"
	scalingRestoreText    = "還原 GPU 縮放設定"
	scalingPrefix         = "GPU 縮放："
	scalingUnreadText     = "尚未讀取"
	scalingUnreadablePre  = "無法讀取——"
	scalingCyclingText    = "正在變更 GPU 縮放…"
	scalingStaticReminder = "，與恢復後的比例不同，請確認已啟用全螢幕縮放。"

	// scalingOverrideNote is permanent, and it is not a note to be replaced by a
	// feature later. The NVIDIA control panel's 「覆寫遊戲和程式所設定的縮放模式」 has no
	// NVAPI interface at all: this tool can neither read it nor set it, and a game is
	// free to override the value that was just written. A button that cannot promise
	// what the user actually wants has to say so where the button is.
	scalingOverrideNote = "若遊戲內仍有黑邊，請到 NVIDIA 控制台勾選" +
		"「覆寫遊戲和程式所設定的縮放模式」——這一項工具無法代為設定。"

	scalingUnconfiguredReason = "尚未設定顯示器，無法讀取或變更 GPU 縮放。"
	scalingUnprobedReason     = "尚未讀取 GPU 縮放設定，請按「重新整理」。"
	scalingCycleReason        = "正在變更 GPU 縮放，請稍候。"
	scalingRecoveryReason     = "顯示配置仍待恢復；請先恢復原始顯示模式，再變更 GPU 縮放。"

	scalingApplyOperation   = "變更 GPU 縮放"
	scalingRestoreOperation = "還原 GPU 縮放設定"
	exitScalingOperation    = "結束前還原 GPU 縮放設定"

	// restoreButtonText carries no numbers. The mode a restore applies depends on the
	// profile and on what the monitor reports, so it is rendered into the button's
	// tooltip from each snapshot instead of frozen into its caption.
	restoreButtonText = "恢復原始解析度"

	trayShowText    = "顯示主視窗"
	trayRefreshText = "重新整理狀態"
	trayExitText    = "結束"

	// enableOperation names the operation rather than the mode. It is only ever seen
	// as the subject of a failure dialog ("...失敗："), where the mode that failed is
	// already in the error text underneath it.
	enableOperation      = "套用顯示模式"
	restoreOperation     = "恢復顯示模式"
	refreshOperation     = "重新整理顯示狀態"
	reloadOperation      = "重新讀取設定檔"
	openFolderOperation  = "開啟設定檔所在資料夾"
	resetOperation       = "重新設定"
	autoRestoreOperation = "自動恢復顯示模式"
	shutdownOperation    = "結束前恢復顯示模式"

	// Two designs each added to this form -- the profile work brought the 自動恢復 line
	// and a fourth button, the scaling work a value line, a button with its reason
	// beside it and the measured reminder. They were laid out together once rather
	// than a line at a time, which is why these numbers jumped instead of creeping.
	windowWidth  = 560
	windowHeight = 600

	// reasonLineBudget and reasonLineLimit are what keep a fixed-size window fixed.
	// NVAPI rejections are long sentences this tool does not author, and a smoke test
	// has already caught one widening the form; folding them to a known rune count
	// before they reach Walk makes that structurally impossible. Nothing is lost --
	// the untouched text is the label's tooltip.
	reasonLineBudget = 34
	reasonLineLimit  = 3

	// statusLineLimit and adviceLineLimit are the same bound for the two taller labels.
	statusLineLimit   = 5
	adviceLineLimit   = 4
	detailLineLimit   = 5
	overrideLineLimit = 3

	// Shell balloons are asynchronous. Keep the notification icon alive long enough
	// for Windows to present a failed scaling-restore warning before process exit.
	exitWarningLifetime = 5 * time.Second

	// monitorLabelBudget is how much of a monitor's name the fixed-width 目標螢幕 line
	// shows before it is elided. Nothing is lost: the whole name is the line's tooltip.
	monitorLabelBudget = 30

	// trayTooltipBudget keeps the notification-area tip inside NOTIFYICONDATA.szTip,
	// which is 128 wide characters including the terminator. walk copies into that
	// array with copy(), so a longer string is truncated without a NUL and the shell
	// renders whatever follows it.
	trayTooltipBudget = 120

	// unnamedMonitor is the last rung of the naming ladder: a profile that carries no
	// label and no identity strings at all. It names the role rather than inventing a
	// monitor, because the window has nothing to invent one from.
	unnamedMonitor = "設定的顯示器"
)

type settingsDialogOpener func(*settingsModel, func(domain.Profile) error) (bool, error)

// window owns every Walk object. All of its fields are read and written on the
// Walk UI thread only.
type window struct {
	provider     *app.Provider
	displays     display.Controller
	processes    processcheck.Lister
	openSettings settingsDialogOpener
	configState  windowConfigState

	mw   *walk.MainWindow
	tray *walk.NotifyIcon

	targetLabel      *walk.Label
	modeLabel        *walk.Label
	toggle           *walk.CheckBox
	autoRestoreLabel *walk.Label
	statusLabel      *walk.TextLabel
	scalingLabel     *walk.Label
	scalingButton    *walk.PushButton
	scalingAdvice    *walk.TextLabel
	scalingDetails   *walk.TextLabel
	scalingOverride  *walk.TextLabel
	settingsReason   *walk.TextLabel
	settingsButton   *walk.PushButton
	refreshButton    *walk.PushButton
	openFolderButton *walk.PushButton
	resetButton      *walk.PushButton
	hideButton       *walk.PushButton
	restoreButton    *walk.PushButton

	showAction     *walk.Action
	enableAction   *walk.Action
	restoreAction  *walk.Action
	refreshAction  *walk.Action
	openAction     *walk.Action
	resetAction    *walk.Action
	settingsAction *walk.Action
	exitAction     *walk.Action

	busy           bool
	suppressToggle bool

	// The two availability groups are two fields on purpose. One latched string used
	// to feed one interactive flag, so a driver that could not answer about GPU
	// scaling disabled the display-mode toggle with it -- a control that has nothing
	// to do with the GPU scaling API. Neither field is ever written from the other
	// half's failure.
	unavailableReason        string
	scalingUnavailableReason string

	// reportedAutoRestoreFailures is the highest Snapshot.AutoRestoreFailures this
	// window has already announced.
	reportedAutoRestoreFailures uint64
	observedSession             *app.Session
}

// Run builds the main window plus the notification-area icon and blocks on the
// Walk message loop until the tray exit action ends the application.
func Run(provider *app.Provider, displays display.Controller, processes processcheck.Lister) error {
	if provider == nil {
		return errors.New("ui: provider must not be nil")
	}
	if displays == nil {
		return errors.New("ui: display controller must not be nil")
	}
	if processes == nil {
		return errors.New("ui: process lister must not be nil")
	}

	w := &window{provider: provider, displays: displays, processes: processes}
	w.openSettings = func(model *settingsModel, replace func(domain.Profile) error) (bool, error) {
		return showSettingsDialog(w.mw, model, replace)
	}
	if err := w.buildMainWindow(); err != nil {
		return err
	}
	if err := w.buildTray(); err != nil {
		return err
	}
	defer func() { _ = w.tray.Dispose() }()

	w.render(provider.Snapshot())
	provider.SetOnChange(func(notified app.Snapshot) {
		// The notification is only a wake-up. A replacement may overtake the queued UI
		// closure, so it reads the provider again on the UI thread instead of rendering
		// the snapshot captured by an old session's callback.
		w.mw.Synchronize(func() { w.render(snapshotForRender(provider, notified)) })
	})
	w.mw.Starting().Once(func() {
		if shouldOpenFirstRun(provider) {
			w.onSettings(true)
		}
	})

	// Startup is read-only. Refresh talks to Win32, so it runs off the UI thread
	// and only reports what it found; it never changes a display mode.
	go func() {
		// A failed startup probe needs no dialog: it is rendered into the status
		// label, and the refresh command stays live so the user can read again.
		if session := provider.Session(); session != nil {
			_, _ = session.Refresh()
			w.mw.Synchronize(func() { w.render(provider.Snapshot()) })
		}
	}()

	// walk creates the form hidden (WS_OVERLAPPEDWINDOW has no WS_VISIBLE), so the
	// main window is shown explicitly before the message loop starts.
	w.mw.Show()
	w.mw.Run()
	return nil
}

func snapshotForRender(provider *app.Provider, _ app.Snapshot) app.Snapshot {
	return provider.Snapshot()
}

func (w *window) buildMainWindow() error {
	// Every Text below is a placeholder for the single render that follows Create;
	// none of them is ever the string the user reads.
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
			dec.Label{AssignTo: &w.targetLabel, Text: "目標螢幕：讀取中"},
			dec.Label{AssignTo: &w.modeLabel, Text: "目前模式：讀取中"},
			dec.CheckBox{
				AssignTo:         &w.toggle,
				Text:             "套用設定的顯示模式",
				OnCheckedChanged: w.onToggled,
			},
			dec.Label{AssignTo: &w.autoRestoreLabel, Text: "自動恢復：讀取中"},
			dec.Label{AssignTo: &w.scalingLabel, Text: scalingPrefix + scalingUnreadText},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.PushButton{AssignTo: &w.scalingButton, Text: scalingApplyText, OnClicked: w.onScaling},
					dec.HSpacer{},
				},
			},
			// The reason a disabled button is disabled, and the measured reminder, share
			// one full-width label directly under the button. They are beside the button
			// in the sense the spec means -- on screen next to it, never only in a
			// tooltip -- and putting them in the button's own row instead would not fit:
			// the restore caption already carries a scaling value, and an NVAPI rejection
			// is a whole sentence. This is also the layout the spec's own sketch shows.
			dec.TextLabel{AssignTo: &w.scalingAdvice, Text: "", MinSize: dec.Size{Height: 64}},
			dec.TextLabel{AssignTo: &w.scalingDetails, Text: "", MinSize: dec.Size{Height: 80}},
			dec.TextLabel{AssignTo: &w.scalingOverride, Text: scalingOverrideNote, MinSize: dec.Size{Height: 48}},
			dec.TextLabel{
				AssignTo: &w.statusLabel,
				Text:     "狀態：啟動中",
				MinSize:  dec.Size{Height: 48},
			},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.PushButton{AssignTo: &w.settingsButton, Text: settingsText, OnClicked: func() { w.onSettings(false) }},
					dec.TextLabel{AssignTo: &w.settingsReason, Text: "", MinSize: dec.Size{Height: 24}},
				},
			},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.PushButton{AssignTo: &w.openFolderButton, Text: openConfigFolderText, OnClicked: w.onOpenFolder},
					dec.PushButton{AssignTo: &w.resetButton, Text: resetConfigText, OnClicked: w.onReset},
					dec.HSpacer{},
				},
			},
			dec.VSpacer{},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.HSpacer{},
					dec.PushButton{AssignTo: &w.refreshButton, Text: refreshText, OnClicked: w.onRefresh},
					dec.PushButton{AssignTo: &w.hideButton, Text: hideText, OnClicked: w.onHide},
					dec.PushButton{AssignTo: &w.restoreButton, Text: restoreButtonText, OnClicked: w.onRestore},
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

// freezeSize drops the resize and maximise affordances so the window keeps the one
// fixed layout windowWidth x windowHeight describes.
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
	// The enable item's text is a placeholder; render rewrites it from the profile.
	if w.enableAction, err = newAction("套用設定的顯示模式", w.onEnable); err != nil {
		return err
	}
	if w.restoreAction, err = newAction(restoreButtonText, w.onRestore); err != nil {
		return err
	}
	if w.refreshAction, err = newAction(trayRefreshText, w.onRefresh); err != nil {
		return err
	}
	if w.openAction, err = newAction(openConfigFolderText, w.onOpenFolder); err != nil {
		return err
	}
	if w.resetAction, err = newAction(resetConfigText, w.onReset); err != nil {
		return err
	}
	if w.settingsAction, err = newAction(settingsText, func() { w.onSettings(false) }); err != nil {
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
		w.openAction,
		w.resetAction,
		w.settingsAction,
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
		w.runOperation(enableOperation, func() error {
			session, err := w.currentSession()
			if err != nil {
				return err
			}
			return session.Enable()
		})
		return
	}
	w.onRestore()
}

func (w *window) onEnable() {
	w.runOperation(enableOperation, func() error {
		session, err := w.currentSession()
		if err != nil {
			return err
		}
		return session.Enable()
	})
}

func (w *window) onRestore() {
	w.runOperation(restoreOperation, func() error {
		session, err := w.currentSession()
		if err != nil {
			return err
		}
		return session.Disable()
	})
}

// onScaling presses whichever of the two scaling commands the current ownership makes
// available. The window checks its own policy first for the same reason every other
// command does: the session would refuse anyway, and a refusal the user cannot see is
// worse than a button that was never offered.
func (w *window) onScaling() {
	if w.busy {
		return
	}
	snapshot := w.provider.Snapshot()
	w.syncConfigState()
	w.updateScalingAvailability(snapshot)
	available := w.availableControls(snapshot)

	if snapshot.Scaling.Owned {
		if !available.scalingRestore {
			return
		}
		w.runOperation(scalingRestoreOperation, func() error {
			session, err := w.currentSession()
			if err != nil {
				return err
			}
			return session.RestoreGPUScaling()
		})
		return
	}
	if !available.scalingApply {
		return
	}
	w.runOperation(scalingApplyOperation, func() error {
		session, err := w.currentSession()
		if err != nil {
			return err
		}
		return session.ApplyGPUScaling()
	})
}

func (w *window) onRefresh() {
	operation := refreshOperation
	if !w.provider.Configured() {
		operation = reloadOperation
	}
	w.runOperation(operation, w.refresh)
}
func (w *window) onOpenFolder() {
	w.runOperation(openFolderOperation, w.provider.OpenConfigFolder)
}
func (w *window) onReset() {
	if w.busy {
		return
	}
	snapshot := w.provider.Snapshot()
	w.syncConfigState()
	if !w.availableControls(snapshot).reset {
		return
	}
	w.showMainWindow()
	w.busy = true
	w.render(snapshot)

	accepted, _, err := resetAndOpenSettings(
		func() bool {
			return walk.MsgBox(
				w.mw,
				windowTitle,
				"要先備份目前讀不懂的設定檔，再重新設定嗎？",
				walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxSetForeground,
			) == walk.DlgCmdYes
		},
		w.provider.Reset,
		func(backupPath string) (bool, error) {
			walk.MsgBox(
				w.mw,
				windowTitle,
				"原設定檔已備份至：\n"+backupPath,
				walk.MsgBoxOK|walk.MsgBoxIconInformation|walk.MsgBoxSetForeground,
			)
			return w.showSettings(true)
		},
	)
	w.busy = false
	snapshot = w.provider.Snapshot()
	w.render(snapshot)
	if err != nil {
		w.reportError(resetOperation, err, snapshot)
		return
	}
	if accepted {
		w.runOperation(refreshOperation, w.refresh)
	}
}
func (w *window) onSettings(firstRun bool) {
	if w.busy {
		return
	}
	snapshot := w.provider.Snapshot()
	w.syncConfigState()
	if !w.availableControls(snapshot).settings {
		return
	}
	w.showMainWindow()
	w.busy = true
	w.render(snapshot)

	accepted, err := w.showSettings(firstRun)
	w.busy = false
	snapshot = w.provider.Snapshot()
	w.render(snapshot)
	if err != nil {
		w.reportError("開啟設定", err, snapshot)
		return
	}
	if accepted {
		// Saving and replacing have already succeeded. This read is a separate status
		// refresh, so a display read failure can never turn into a second config.Save.
		w.runOperation(refreshOperation, w.refresh)
	}
}
func (w *window) onHide() { w.hideToTray() }
func (w *window) onShow() { w.showMainWindow() }

func (w *window) showSettings(firstRun bool) (bool, error) {
	if w.provider == nil || w.displays == nil || w.processes == nil || w.openSettings == nil {
		return false, errors.New("設定介面缺少必要的執行元件")
	}
	snapshot := w.provider.Snapshot()
	profile := snapshot.Profile
	if firstRun {
		profile = domain.Profile{}
	}
	model := newSettingsModel(w.displays, w.processes, profile, snapshot.Scaling, firstRun)
	model.readScaling = w.provider.ReadScaling
	return w.openSettings(model, w.provider.SaveAndReplace)
}

func shouldOpenFirstRun(provider *app.Provider) bool {
	return provider != nil && provider.Unconfigured()
}

func resetAndOpenSettings(
	confirm func() bool,
	reset func() (string, error),
	open func(backupPath string) (bool, error),
) (accepted bool, backupPath string, err error) {
	if !confirm() {
		return false, "", nil
	}
	backupPath, err = reset()
	if err != nil {
		return false, "", err
	}
	accepted, err = open(backupPath)
	return accepted, backupPath, err
}

// refresh re-reads the display state through the session. It is the way out of
// the unavailable latch: a monitor that was asleep or on another input when the
// tool started leaves every mutating control disabled, and only a fresh read can
// tell the window that the monitor came back.
func (w *window) refresh() error {
	if !w.provider.Configured() {
		if err := w.provider.Reload(); err != nil {
			return err
		}
	}
	session := w.provider.Session()
	if session == nil {
		return nil
	}
	_, err := session.Refresh()
	return err
}

func (w *window) currentSession() (*app.Session, error) {
	if w.provider == nil {
		return nil, errors.New("尚未設定")
	}
	session := w.provider.Session()
	if session == nil {
		return nil, errors.New("尚未設定")
	}
	return session, nil
}

// onExit restores any mode this run applied before the process ends. A failed
// restore keeps the application alive so the user can retry instead of silently
// abandoning the configured mode.
func (w *window) onExit() {
	if w.busy {
		return
	}
	w.busy = true
	w.render(w.provider.Snapshot())
	retiring := w.provider.Session()

	go func() {
		err := w.provider.Shutdown()
		snapshot := w.provider.Snapshot()
		snapshot.Scaling = shutdownScaling(w.provider, retiring)
		w.mw.Synchronize(func() {
			w.busy = false
			w.render(snapshot)
			if err != nil {
				w.reportError(shutdownOperation, err, snapshot)
				return
			}
			warning := exitScalingWarning(snapshot.Scaling)
			visible := w.mw.Visible()
			if warning != "" {
				w.reportNotice(exitScalingOperation, warning)
			}
			if lifetime := exitNoticeLifetime(warning, visible); lifetime > 0 {
				time.AfterFunc(lifetime, func() { w.mw.Synchronize(w.finishExit) })
				return
			}
			w.finishExit()
		})
	}()
}

func (w *window) finishExit() {
	_ = w.tray.Dispose()
	walk.App().Exit(0)
}

func exitNoticeLifetime(warning string, windowVisible bool) time.Duration {
	if warning != "" && !windowVisible {
		return exitWarningLifetime
	}
	return 0
}

// shutdownScaling is the scaling row as it stands after a shutdown, which is not the
// same thing as the row the Provider is holding.
//
// A scaling restore that fails at exit deliberately does not fail Shutdown: the value
// is runtime-only and a reboot undoes it, while a tool that cannot be closed is a
// worse outcome. The signal is structural instead -- this run still owns a change it
// did not put back -- and it has to be read from the session that was retired, because
// Provider clears its session pointer before shutting that session down and therefore
// stops recording anything the exit path publishes. Reading Provider alone would warn
// about every successful restore.
func shutdownScaling(provider *app.Provider, retired *app.Session) app.ScalingSnapshot {
	if retired != nil {
		return retired.Snapshot().Scaling
	}
	if provider != nil {
		return provider.Snapshot().Scaling
	}
	return app.ScalingSnapshot{}
}

// exitScalingWarning is what the user is told about a scaling value the tool changed
// and could not put back. It never claims anything is broken: the value is not
// persisted, so it says what will undo it without the user doing anything.
func exitScalingWarning(view app.ScalingSnapshot) string {
	if !view.Owned {
		return ""
	}
	return "結束前還原 GPU 縮放設定失敗，目前仍是本工具寫入的值。\n" +
		"這個值不會被保存：重新開機或重新載入顯示卡驅動就會回到「" + app.ScalingLabel(view.Saved) + "」，" +
		"也可以到 NVIDIA 控制台手動改回。"
}

// runOperation disables the mutating controls, performs one session operation off
// the UI thread, then re-renders and reports failures on the UI thread.
func (w *window) runOperation(operation string, action func() error) {
	if w.busy {
		return
	}
	w.busy = true
	w.render(w.provider.Snapshot())

	go func() {
		err := action()
		snapshot := w.provider.Snapshot()
		w.mw.Synchronize(func() {
			w.busy = false
			w.render(snapshot)
			if err != nil && !errors.Is(err, app.ErrClosed) {
				w.reportError(operation, err, snapshot)
			}
		})
	}()
}

func (w *window) reportError(operation string, err error, snapshot app.Snapshot) {
	message := operation + "失敗：\n" + err.Error()
	if note := deviceNote(snapshot, err.Error()); note != "" {
		message += "\n" + note
	}
	if w.mw.Visible() {
		walk.MsgBox(w.mw, windowTitle, message, walk.MsgBoxIconError|walk.MsgBoxSetForeground)
		return
	}
	// Hidden in the notification area: a balloon keeps a failed restore visible.
	_ = w.tray.ShowError(windowTitle, message)
}

// reportNotice is reportError's non-error twin: something the user has to be told
// once, through whichever surface they are actually looking at.
func (w *window) reportNotice(operation, message string) {
	text := operation + "：\n" + message
	if w.mw.Visible() {
		walk.MsgBox(w.mw, windowTitle, text, walk.MsgBoxIconWarning|walk.MsgBoxSetForeground)
		return
	}
	_ = w.tray.ShowWarning(windowTitle, text)
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
	w.syncConfigState()
	if w.configState != configStateConfigured {
		w.unavailableReason = ""
	}
	w.updateAvailability(snapshot)
	w.updateScalingAvailability(snapshot)

	_ = w.targetLabel.SetText(targetText(snapshot))
	_ = w.targetLabel.SetToolTipText(targetTooltip(snapshot))
	_ = w.modeLabel.SetText(modeText(snapshot))
	_ = w.toggle.SetText(toggleText(snapshot))
	_ = w.autoRestoreLabel.SetText(autoRestoreText(snapshot))
	status := w.statusText(snapshot)
	// Folded here rather than in statusText: the sentence a person reads is the one the
	// session wrote, and every test that asserts a message survived the window intact
	// asserts it against the unfolded string.
	_ = w.statusLabel.SetText(fitText(status, reasonLineBudget, statusLineLimit))
	_ = w.statusLabel.SetToolTipText(status)
	w.renderScaling(snapshot)
	_ = w.restoreButton.SetToolTipText(restoreTooltip(snapshot))
	_ = w.refreshButton.SetText(w.refreshCaption())
	settingsReason := settingsDisabledReason(snapshot)
	_ = w.settingsReason.SetText(settingsReason)
	w.settingsReason.SetVisible(settingsReason != "")
	_ = w.settingsButton.SetToolTipText(settingsReason)

	_ = w.enableAction.SetText(trayEnableText(snapshot))
	_ = w.refreshAction.SetText(w.refreshCaption())
	_ = w.settingsAction.SetText(settingsActionText(snapshot))
	_ = w.settingsAction.SetToolTip(settingsReason)
	_ = w.tray.SetToolTip(trayTooltip(snapshot))

	// While an operation runs the checkbox reflects the user intent, not the
	// transient session state.
	if !w.busy {
		w.setToggleChecked(snapshot.AtGameMode)
	}
	w.applyEnabled(snapshot)

	if err := w.takeAutoRestoreFailure(snapshot); err != nil {
		w.reportError(autoRestoreOperation, err, snapshot)
	}
}

type windowConfigState uint8

const (
	// Configured is deliberately the zero value so the existing pure rendering tests,
	// which construct a window without a live Provider, continue to describe a loaded
	// profile. Tests for the other two states opt into them explicitly.
	configStateConfigured windowConfigState = iota
	configStateUnconfigured
	configStateReadOnly
)

func (w *window) syncConfigState() {
	if w.provider == nil {
		return
	}
	current := w.provider.Session()
	if current != w.observedSession {
		w.observedSession = current
		w.reportedAutoRestoreFailures = 0
	}
	switch {
	case w.provider.Configured():
		w.configState = configStateConfigured
	case w.provider.ReadOnly():
		w.configState = configStateReadOnly
	default:
		w.configState = configStateUnconfigured
	}
}

func (w *window) refreshCaption() string {
	if w.configState == configStateConfigured {
		return refreshText
	}
	return reloadText
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

// updateAvailability latches the conditions that must disable the mode toggle. Three
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
//
// Every one of the four names the configured monitor or the configured mode, taken
// from the snapshot. The version this replaced spelled the shipped profile's numbers
// into the unsupported-mode sentence, and told them to every user -- including the
// ones who had configured neither of them.
func (w *window) updateAvailability(snapshot app.Snapshot) {
	switch {
	case errors.Is(snapshot.Err, display.ErrTargetNotFound):
		w.unavailableReason = "找不到 " + monitorLabelShort(snapshot) + "，已停用顯示模式切換。"
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
		w.unavailableReason = "這台顯示器目前沒有回報 " + domain.ModeLabel(snapshot.Profile.GameMode) +
			"，已停用顯示模式切換。"
	case snapshot.Err == nil && snapshot.Target.DeviceName != "":
		w.unavailableReason = ""
	}
}

// updateScalingAvailability is the second availability group, and it is derived rather
// than latched: the session re-probes on every refresh and on every scaling workflow,
// so snapshot.Scaling is already the current answer and a latch would only hold a
// stale one. It reads snapshot.Scaling and the target, and nothing else -- no display
// failure that is about a display mode may appear here, and nothing here may ever be
// written into unavailableReason.
//
// The target is consulted for one reason the scaling spec spells out: NVAPI is asked
// about a monitor, so a monitor the tool cannot resolve at all is this button's own
// unavailability too. It gets its own sentence rather than borrowing the mode toggle's,
// because the two commands that are off are different commands. A mode the monitor does
// not report, by contrast, is purely a display-side refusal and never reaches here.
func (w *window) updateScalingAvailability(snapshot app.Snapshot) {
	view := snapshot.Scaling
	switch {
	case w.configState != configStateConfigured:
		w.scalingUnavailableReason = scalingUnconfiguredReason
	case snapshot.RecoveryPending:
		w.scalingUnavailableReason = scalingRecoveryReason
	case unresolvedTarget(snapshot):
		w.scalingUnavailableReason = "目前無法對應到 " + monitorLabelShort(snapshot) +
			"，無法讀取或變更它的 GPU 縮放。"
	case view.Available:
		w.scalingUnavailableReason = ""
	case strings.TrimSpace(view.Reason) != "":
		// The provider/session has already combined functional availability with the
		// selected monitor's diagnostic metadata. Appending generic text here could
		// duplicate or contradict its vendor-specific advice.
		w.scalingUnavailableReason = strings.TrimSpace(view.Reason)
	default:
		w.scalingUnavailableReason = scalingUnprobedReason
	}
}

// unresolvedTarget reports the three ways the configured monitor cannot be pointed at.
// ErrModeNotSupported is deliberately absent: it says the monitor is there and refuses
// one mode, which is no reason to stop reading its scaling setting.
func unresolvedTarget(snapshot app.Snapshot) bool {
	return errors.Is(snapshot.Err, display.ErrTargetNotFound) ||
		errors.Is(snapshot.Err, display.ErrTargetAmbiguous) ||
		errors.Is(snapshot.Err, display.ErrTargetMirrored)
}

func (w *window) renderScaling(snapshot app.Snapshot) {
	line := scalingText(snapshot)
	_ = w.scalingLabel.SetText(fitText(line, reasonLineBudget, 1))
	_ = w.scalingLabel.SetToolTipText(line)

	_ = w.scalingButton.SetText(scalingButtonText(snapshot))
	_ = w.scalingButton.SetToolTipText(w.scalingButtonNote(snapshot))

	advice := w.scalingAdviceText(snapshot)
	_ = w.scalingAdvice.SetText(fitText(advice, reasonLineBudget, adviceLineLimit))
	_ = w.scalingAdvice.SetToolTipText(advice)
	w.scalingAdvice.SetVisible(advice != "")

	details := scalingDetailsText(snapshot)
	_ = w.scalingDetails.SetText(fitText(details, reasonLineBudget, detailLineLimit))
	_ = w.scalingDetails.SetToolTipText(details)
	w.scalingDetails.SetVisible(details != "")

	override := scalingOverrideText()
	_ = w.scalingOverride.SetText(fitText(override, reasonLineBudget, overrideLineLimit))
	_ = w.scalingOverride.SetToolTipText(override)
}

// scalingAdviceText is the button's own explanation. Scaling measurements and the
// permanent override warning have dedicated visible rows so neither can be pushed into
// a tooltip by a long driver refusal.
func (w *window) scalingAdviceText(snapshot app.Snapshot) string {
	return w.scalingButtonNote(snapshot)
}

// controls says which commands accept input. It is one value so the policy can be
// decided without touching Walk and asserted in a test.
type controls struct {
	toggle         bool
	restore        bool
	enable         bool
	refresh        bool
	openFolder     bool
	reset          bool
	settings       bool
	scalingApply   bool
	scalingRestore bool
	hide           bool
	show           bool
	exit           bool
}

// availableControls keeps the read-only command live while the target monitor is
// unavailable: re-reading is the only way out of that latch. The mode commands stay
// disabled with the reason on screen.
//
// restore has a second condition of its own. A restore of a desktop this session does
// not own applies the fallback mode, and a fallback the session could not derive means
// there is nothing to apply -- the session would refuse the click, so the window
// refuses it first and puts the session's own reason in the tooltip. A session that
// *does* own the desktop is the exception: it restores the arrangement it recorded
// before it applied anything and never consults the fallback, so taking that button
// away would leave the user holding the applied mode with no way out but exiting.
//
// The scaling pair is derived from a separate reason and is otherwise unconditioned by
// the display half. In particular it stays live while the session owns an applied
// mode: that is the only moment a user can see the black bars the button is for, so a
// button disabled exactly then is a button that does not exist. Pressing it runs the
// whole restore-set-reapply cycle, and the line under it says so. `設定…` is the
// deliberate contrast and stays disabled while managed, because a profile change voids
// what the saved arrangement means while a scaling change only threatens the device
// names inside it, which the cycle re-derives.
//
// While the cycle itself runs, every mutating control is off, its own button included.
// opMu already guarantees correctness -- a second click merely queues -- but a queued
// command against a desktop that is mid-change is not something to offer. Refresh and
// 開啟設定檔所在資料夾 are reads; hide and show change nothing on the desktop. Exit
// starts a shutdown workflow and is therefore mutating, so it is off too.
func (w *window) availableControls(snapshot app.Snapshot) controls {
	configured := w.configState == configStateConfigured
	cycling := snapshot.State == app.StateScalingCycle
	idle := !w.busy && !cycling
	interactive := configured && w.unavailableReason == "" && idle
	applicable := interactive && !snapshot.RecoveryPending
	scalable := configured && w.scalingUnavailableReason == "" && idle
	return controls{
		toggle:     applicable,
		restore:    interactive && (snapshot.Managed || snapshot.RecoveryPending || snapshot.FallbackKnown),
		enable:     applicable && !snapshot.AtGameMode,
		refresh:    !w.busy,
		openFolder: !w.busy,
		reset:      idle && w.configState == configStateReadOnly,
		settings: idle && !snapshot.Managed && !snapshot.RecoveryPending && !snapshot.Scaling.Owned &&
			w.configState != configStateReadOnly,
		scalingApply:   scalable && !snapshot.RecoveryPending && !snapshot.Scaling.Owned,
		scalingRestore: scalable && !snapshot.RecoveryPending && snapshot.Scaling.Owned,
		hide:           true,
		show:           true,
		exit:           idle,
	}
}

func (w *window) applyEnabled(snapshot app.Snapshot) {
	available := w.availableControls(snapshot)

	w.toggle.SetEnabled(available.toggle)
	w.restoreButton.SetEnabled(available.restore)
	w.refreshButton.SetEnabled(available.refresh)
	w.openFolderButton.SetEnabled(available.openFolder)
	w.resetButton.SetEnabled(available.reset)
	w.resetButton.SetVisible(w.configState == configStateReadOnly)
	w.settingsButton.SetEnabled(available.settings)
	w.scalingButton.SetEnabled(available.scalingApply || available.scalingRestore)
	w.hideButton.SetEnabled(available.hide)

	_ = w.showAction.SetEnabled(available.show)
	_ = w.enableAction.SetEnabled(available.enable)
	_ = w.restoreAction.SetEnabled(available.restore)
	_ = w.refreshAction.SetEnabled(available.refresh)
	_ = w.openAction.SetEnabled(available.openFolder)
	_ = w.resetAction.SetEnabled(available.reset)
	_ = w.resetAction.SetVisible(w.configState == configStateReadOnly)
	_ = w.settingsAction.SetEnabled(available.settings)
	_ = w.exitAction.SetEnabled(available.exit)
}

func settingsDisabledReason(snapshot app.Snapshot) string {
	if snapshot.RecoveryPending {
		return settingsRecoveryText
	}
	if snapshot.Managed {
		return settingsManagedText
	}
	if snapshot.Scaling.Owned {
		return settingsScalingText
	}
	return ""
}

func settingsActionText(snapshot app.Snapshot) string {
	if reason := settingsDisabledReason(snapshot); reason != "" {
		return settingsText + "（" + reason + "）"
	}
	return settingsText
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
	message := snapshot.Message
	if message == "" {
		message = stateText(snapshot)
	}

	var builder strings.Builder
	builder.WriteString("狀態：")
	builder.WriteString(message)
	if note := deviceNote(snapshot, message); note != "" {
		builder.WriteString("\n")
		builder.WriteString(note)
	}
	if w.unavailableReason != "" {
		builder.WriteString("\n")
		builder.WriteString(w.unavailableReason)
	}
	return builder.String()
}

// monitorLabel is the name the user gave the configured monitor, and the ladder the
// window climbs down when there is not one. It never invents a name: a profile with no
// label is described by the key it is actually matched on, which is something the user
// can compare against their hardware, and only a profile with no identity at all falls
// through to naming the role.
func monitorLabel(snapshot app.Snapshot) string {
	identity := snapshot.Profile.Monitor
	switch {
	case strings.TrimSpace(identity.Label) != "":
		return strings.TrimSpace(identity.Label)
	case identity.InstancePath != "":
		return identity.InstancePath
	case identity.HardwareID != "":
		return identity.HardwareID
	default:
		return unnamedMonitor
	}
}

func monitorLabelShort(snapshot app.Snapshot) string {
	return shortenMiddle(monitorLabel(snapshot), monitorLabelBudget)
}

// shortenMiddle keeps both ends of a string that does not fit and elides the middle.
//
// A monitor label is legitimately allowed to be a whole device interface path --
// MONITOR\XMI27B2\{4d36e96e-e325-11ce-bfc1-08002be10318}\0009 -- because that is what
// the tool falls back to when the monitor reports no friendlier name, and the window
// is a fixed 460x280. Cutting the tail would delete the \0009 that says which port the
// monitor is on, which is the only part that distinguishes two units of one model;
// cutting the head would delete the model. The middle is the device class GUID, which
// is byte-for-byte identical on every monitor on every machine, so it is the one part
// nobody can miss. The whole string is never lost -- it is the label's tooltip.
func shortenMiddle(text string, budget int) string {
	runes := []rune(text)
	if budget < 3 || len(runes) <= budget {
		return text
	}
	keep := budget - 1
	head := (keep + 1) / 2
	return string(runes[:head]) + "…" + string(runes[len(runes)-(keep-head):])
}

func targetText(snapshot app.Snapshot) string {
	label := monitorLabelShort(snapshot)
	if snapshot.Target.DeviceName == "" {
		return "目標螢幕：" + label + "（尚未找到）"
	}
	return "目標螢幕：" + label + " → " + snapshot.Target.DeviceName
}

// targetTooltip is where the untruncated name lives, so shortening the line on screen
// never costs the user information.
func targetTooltip(snapshot app.Snapshot) string {
	label := monitorLabel(snapshot)
	if snapshot.Target.DeviceName == "" {
		return label + "（尚未找到）"
	}
	return label + " → " + snapshot.Target.DeviceName
}

func modeText(snapshot app.Snapshot) string {
	mode := snapshot.CurrentMode
	if mode.Width == 0 || mode.Height == 0 {
		return "目前模式：未知"
	}
	return fmt.Sprintf("目前模式：%s（%d bpp）", domain.ModeLabel(mode), mode.BitsPerPixel)
}

// toggleText names the mode the checkbox applies and the shape of it, because the
// shape is what the user was choosing when they picked it. The guard is for a profile
// with no mode at all, which validation rejects and which therefore only a zero-valued
// snapshot produces; "使用 未知的顯示模式" is not a sentence to put on a checkbox.
func toggleText(snapshot app.Snapshot) string {
	mode := snapshot.Profile.GameMode
	if mode.Width == 0 || mode.Height == 0 {
		return "套用設定的顯示模式"
	}
	return fmt.Sprintf("使用 %s（%s）", domain.ModeLabel(mode), domain.AspectLabel(mode.Width, mode.Height))
}

// trayEnableText is the notification menu's version of the checkbox, without the
// aspect: a context menu is read at a glance and the numbers are the identifying part.
func trayEnableText(snapshot app.Snapshot) string {
	mode := snapshot.Profile.GameMode
	if mode.Width == 0 || mode.Height == 0 {
		return "套用設定的顯示模式"
	}
	return "使用 " + domain.ModeLabel(mode)
}

// trayTooltip says which configuration this icon belongs to, so hovering answers
// "which monitor, which mode" without opening the window.
func trayTooltip(snapshot app.Snapshot) string {
	tip := windowTitle + "\n" + monitorLabel(snapshot)
	if mode := snapshot.Profile.GameMode; mode.Width != 0 && mode.Height != 0 {
		tip += "\n" + domain.ModeLabel(mode)
	}
	return shortenMiddle(tip, trayTooltipBudget)
}

// autoRestoreText states the watch the profile arms, including the case where it arms
// none. An empty process name is a legal configuration -- manual switching only -- and
// the one thing that must never happen is a user believing they configured a watch
// that will not fire, so the unset case says both what it is and what it means.
func autoRestoreText(snapshot app.Snapshot) string {
	name := strings.TrimSpace(snapshot.Profile.ProcessName)
	if name == "" {
		return "自動恢復：未設定（不會自動恢復）"
	}
	if snapshot.Profile.RestoreDelay <= 0 {
		return "自動恢復：" + name + "，結束後立即恢復"
	}
	seconds := strconv.FormatFloat(snapshot.Profile.RestoreDelay.Seconds(), 'f', -1, 64)
	return "自動恢復：" + name + "，結束後 " + seconds + " 秒"
}

// restoreTooltip says what the restore button will actually do, which is three
// different things.
//
// An owned desktop is put back to the arrangement the session recorded before it
// applied anything -- mode and every display's coordinates together -- and that mode
// is not in the snapshot, so the tooltip describes it rather than printing numbers it
// would have to guess at. An unowned desktop gets the fallback, which is printed
// exactly. A fallback that could not be derived disables the button, and the session's
// own reason is what the tooltip carries: the two ways the derivation fails are
// different events -- a monitor that stopped answering, and a monitor that answered
// with nothing usable -- and flattening them here would send half of the users looking
// for the wrong thing.
func restoreTooltip(snapshot app.Snapshot) string {
	if snapshot.Managed {
		return "恢復這個工具套用前記下的顯示模式與桌面排列"
	}
	if snapshot.FallbackKnown {
		return "恢復為 " + domain.ModeLabel(snapshot.FallbackMode)
	}
	if reason := strings.TrimSpace(snapshot.FallbackReason); reason != "" {
		return reason
	}
	return "尚未讀取顯示器回報的顯示模式，還不知道要恢復成哪一個"
}

// deviceNote translates the one \\.\DISPLAYn a message can be about into the name the
// user chose for it.
//
// internal/display's arrangement planner writes its refusals in Win32 device names
// ("\\.\DISPLAY6 is not attached") because it plans against a layout and has no access
// to monitor labels, and a device name is not something anybody recognises. The window
// cannot label the other screens either -- it only ever sees the configured monitor's
// identity -- so this is a partial fix on purpose: it names the one display the user
// configured, and stays silent about a message that does not mention it rather than
// guessing.
func deviceNote(snapshot app.Snapshot, text string) string {
	device := snapshot.Target.DeviceName
	if device == "" || !strings.Contains(text, device) {
		return ""
	}
	return "（" + monitorLabelShort(snapshot) + " 目前是 " + device + "）"
}

// stateText is the sentence for a snapshot that carries no message of its own. Every
// branch is written from the profile, so the state the window falls back to describes
// the same configuration the message it replaced would have.
func stateText(snapshot app.Snapshot) string {
	mode := domain.ModeLabel(snapshot.Profile.GameMode)
	switch snapshot.State {
	case app.StateNative:
		return "尚未套用 " + mode
	case app.StateApplying:
		return "正在套用 " + mode
	case app.StateManualOnly:
		// The profile watches nothing, so nothing will take the mode off again. Saying
		// "等待遊戲" here would promise a restore that was never armed.
		return mode + " 已套用（未設定要監看的程式，不會自動恢復）"
	case app.StateWaitingForGame:
		return mode + " 已套用，等待 " + watchedProcess(snapshot)
	case app.StateGameRunning:
		return watchedProcess(snapshot) + " 執行中"
	case app.StateRestorePending:
		return watchedProcess(snapshot) + " 已結束，等待恢復顯示模式"
	case app.StateRestoring:
		return "正在恢復顯示模式"
	case app.StateScalingCycle:
		// The session names the phase in Message for the whole of the cycle, so this
		// is only the sentence a snapshot without one would get.
		return scalingCyclingText
	case app.StateError:
		return "發生錯誤"
	default:
		return string(snapshot.State)
	}
}

// scalingText is the value line. During the cycle it names the phase rather than a
// result, because there is no result yet and the previous one is about to stop being
// true. Outside it the line is the read-back and only the read-back: a value the tool
// asked for is not a value the driver stored, which was measured rather than assumed.
func scalingText(snapshot app.Snapshot) string {
	if snapshot.State == app.StateScalingCycle {
		if message := strings.TrimSpace(snapshot.Message); message != "" {
			return message
		}
		return scalingCyclingText
	}
	view := snapshot.Scaling
	if view.Known {
		return scalingPrefix + app.ScalingLabel(view.Effective)
	}
	if reason := firstLine(view.Reason); reason != "" {
		return scalingPrefix + scalingUnreadablePre + reason
	}
	return scalingPrefix + scalingUnreadText
}

// scalingButtonText switches on ownership and prints the value a restore would put
// back, because that value is the whole question the user is being asked.
func scalingButtonText(snapshot app.Snapshot) string {
	if !snapshot.Scaling.Owned {
		return scalingApplyText
	}
	return scalingRestoreText + "（" + app.ScalingLabel(snapshot.Scaling.Saved) + "）"
}

// scalingButtonNote is the sentence that accompanies the button: why it is off, or --
// while the session owns a mode -- what pressing it costs. The cost is real and
// visible, so it is stated before the press rather than explained afterwards.
func (w *window) scalingButtonNote(snapshot app.Snapshot) string {
	if snapshot.State == app.StateScalingCycle {
		return scalingCycleReason
	}
	if w.scalingUnavailableReason != "" {
		return w.scalingUnavailableReason
	}
	if snapshot.Managed {
		return scalingManagedCostText(snapshot)
	}
	return ""
}

func scalingManagedCostText(snapshot app.Snapshot) string {
	mode := snapshot.Profile.GameMode
	if mode.Width == 0 || mode.Height == 0 {
		return "按下後畫面會先恢復原始排列、變更縮放、再切回設定的顯示模式。"
	}
	return "按下後畫面會先恢復原始排列、變更縮放、再切回 " + domain.ModeLabel(mode) + "。"
}

// scalingNoteText is the block under the button: what the driver did with the last
// write, what the configured mode will look like, and the one thing this tool cannot
// do for the user.
func scalingNoteText(snapshot app.Snapshot) string {
	return strings.Join(nonEmptyStrings(scalingDetailsText(snapshot), scalingOverrideText()), "\n")
}

func scalingDetailsText(snapshot app.Snapshot) string {
	var lines []string
	if mismatch := scalingMismatchNote(snapshot); mismatch != "" {
		lines = append(lines, mismatch)
	}
	reminder := scalingAspectReminder(snapshot)
	if reminder != "" {
		lines = append(lines, reminder)
	}
	return strings.Join(lines, "\n")
}

func scalingOverrideText() string { return scalingOverrideNote }

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

// scalingMismatchNote states a driver that stored something other than what was
// written. It is not an error and is not worded as one: nothing broke and nothing needs
// undoing. Both values are printed, because only then can the user tell which is which.
func scalingMismatchNote(snapshot app.Snapshot) string {
	view := snapshot.Scaling
	if !view.Known || !view.RequestedKnown || view.Matched {
		return ""
	}
	return "已要求「" + app.ScalingLabel(view.Requested) + "」，驅動實際套用的是「" +
		app.ScalingLabel(view.Effective) + "」。"
}

// scalingAspectReminder is the profile design's static "this is not your panel's shape"
// reminder, upgraded to a measurement now that the scaling value can be read. It never
// blocks the choice -- the user may want the bars, or may have dealt with them
// elsewhere -- and it never claims the bars are gone: full-screen scaling is stated as
// the fact it is, and nothing more, because a game can still override it.
//
// The shape it compares against is the panel's separately enumerated native mode.
// FallbackMode cannot answer this question: it may be an explicit restore override with
// any aspect ratio the user chose.
func scalingAspectReminder(snapshot app.Snapshot) string {
	mode := snapshot.Profile.GameMode
	native := snapshot.NativeMode
	if mode.Width == 0 || mode.Height == 0 || !snapshot.NativeKnown {
		return ""
	}
	if native.Width == 0 || native.Height == 0 {
		return ""
	}
	if sameAspect(mode.Width, mode.Height, native.Width, native.Height) {
		return ""
	}
	shape := "這個模式是 " + domain.AspectLabel(mode.Width, mode.Height)
	if !snapshot.Scaling.Known {
		return shape + scalingStaticReminder
	}
	effective := app.ScalingLabel(snapshot.Scaling.Effective)
	// Compared on the geometry rather than on the raw number, and against the value
	// this product asks for, so the display doing full-screen scaling counts too.
	if snapshot.Scaling.Effective.Mode == app.ScalingFullScreenByGPU().Mode {
		return shape + "，目前的 GPU 縮放是「" + effective + "」。"
	}
	return shape + "，而目前的 GPU 縮放是「" + effective + "」，畫面會有黑邊。"
}

// fitText folds a string into a fixed-width window. Wrapping is by rune count rather
// than by measured pixels because it has to be decidable without a device context and
// assertable in a test that never opens a window; the budget is chosen for the widest
// characters these strings contain, so an ASCII-heavy driver message simply wraps
// earlier than it needs to. Text past maxLines is cut with an ellipsis -- clipped, not
// allowed to push the form wider -- and every caller keeps the whole string in the
// widget's tooltip.
func fitText(text string, budget, maxLines int) string {
	if text == "" || budget <= 0 || maxLines <= 0 {
		return text
	}
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		runes := []rune(line)
		for len(runes) > budget {
			lines = append(lines, string(runes[:budget]))
			runes = runes[budget:]
		}
		lines = append(lines, string(runes))
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] += "…"
	}
	return strings.Join(lines, "\n")
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return text
}

// watchedProcess names the executable the profile watches. The states that call it are
// only reachable with a watch configured; the generic answer is there so a snapshot
// that arrives in an impossible combination still renders a sentence.
func watchedProcess(snapshot app.Snapshot) string {
	if name := strings.TrimSpace(snapshot.Profile.ProcessName); name != "" {
		return name
	}
	return "監看的程式"
}
