//go:build windows

// Package ui renders an app.Session through a native Walk window and a
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

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
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
	autoRestoreOperation = "自動恢復顯示模式"
	shutdownOperation    = "結束前恢復顯示模式"

	// The window gained the 自動恢復 line, and 恢復原始解析度 is a wider button than the
	// 恢復 2K it replaced.
	windowWidth  = 460
	windowHeight = 280

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

// window owns every Walk object. All of its fields are read and written on the
// Walk UI thread only.
type window struct {
	session *app.Session

	mw   *walk.MainWindow
	tray *walk.NotifyIcon

	targetLabel      *walk.Label
	modeLabel        *walk.Label
	toggle           *walk.CheckBox
	autoRestoreLabel *walk.Label
	statusLabel      *walk.TextLabel
	refreshButton    *walk.PushButton
	hideButton       *walk.PushButton
	restoreButton    *walk.PushButton

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

// freezeSize drops the resize and maximise affordances so the window keeps the
// fixed 460x280 layout.
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
// abandoning the configured mode.
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
				w.reportError(shutdownOperation, err, snapshot)
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
	_ = w.targetLabel.SetToolTipText(targetTooltip(snapshot))
	_ = w.modeLabel.SetText(modeText(snapshot))
	_ = w.toggle.SetText(toggleText(snapshot))
	_ = w.autoRestoreLabel.SetText(autoRestoreText(snapshot))
	_ = w.statusLabel.SetText(w.statusText(snapshot))
	_ = w.restoreButton.SetToolTipText(restoreTooltip(snapshot))

	_ = w.enableAction.SetText(trayEnableText(snapshot))
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
func (w *window) availableControls(snapshot app.Snapshot) controls {
	interactive := w.unavailableReason == "" && !w.busy
	return controls{
		toggle:  interactive,
		restore: interactive && (snapshot.Managed || snapshot.FallbackKnown),
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
	case app.StateError:
		return "發生錯誤"
	default:
		return string(snapshot.State)
	}
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
