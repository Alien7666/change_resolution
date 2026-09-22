package ui

import (
	"fmt"

	"github.com/lxn/walk"
	dec "github.com/lxn/walk/declarative"

	"github.com/Alien7666/change_resolution/internal/app"
)

const (
	scalingDialogTitle = "GPU 縮放"
	scalingMenuText    = "GPU 縮放…"
	scalingCloseText   = "關閉"
)

// scalingView is everything the dialog prints, worked out by the window that owns the
// snapshot. The dialog itself decides nothing: keeping the sentences here rather than
// deriving them inside the dialog is what lets the same strings be asserted without a
// message loop.
type scalingView struct {
	value    string
	button   string
	advice   string
	details  string
	override string
	enabled  bool
}

func (w *window) scalingViewFor(snapshot app.Snapshot) scalingView {
	available := w.availableControls(snapshot)
	return scalingView{
		value:    scalingText(snapshot),
		button:   scalingButtonText(snapshot),
		advice:   w.scalingAdviceText(snapshot),
		details:  scalingDetailsText(snapshot),
		override: scalingOverrideText(),
		enabled:  available.scalingApply || available.scalingRestore,
	}
}

// scalingDialog is where GPU scaling is explained and asked for. It exists because the
// three things the tool has to admit about this feature -- that the read-back is the
// driver's record and not the picture, that the value does not survive a mode change,
// and that a non-native mode may or may not be letterboxed -- are paragraphs, and
// paragraphs on the main window buried the two controls a user actually reaches for.
//
// Pressing the button does not act here. It closes the dialog and hands the decision
// back, so the operation runs on the window's ordinary asynchronous path with its
// ordinary busy state, rather than inside a modal loop that would have to reproduce
// all of it.
type scalingDialog struct {
	dialog    *walk.Dialog
	requested bool
}

func runScalingDialog(owner walk.Form, view scalingView) (bool, error) {
	d := &scalingDialog{}
	var act, close *walk.PushButton
	err := dec.Dialog{
		AssignTo: &d.dialog,
		Title:    scalingDialogTitle,
		MinSize:  dec.Size{Width: 460, Height: 360},
		Layout: dec.VBox{
			Margins: dec.Margins{Left: 12, Top: 12, Right: 12, Bottom: 12},
			Spacing: 8,
		},
		Children: []dec.Widget{
			dec.Label{Text: view.value},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.PushButton{
						AssignTo: &act,
						Text:     view.button,
						OnClicked: func() {
							d.requested = true
							d.dialog.Accept()
						},
					},
					dec.HSpacer{},
				},
			},
			// Folded to the same budgets the main window used, so a driver refusal
			// long enough to overflow its row is clipped with an ellipsis rather than
			// pushing the two sentences below it off the dialog. The unfolded text is
			// the tooltip, so nothing is lost.
			dec.TextLabel{Text: fitText(view.advice, reasonLineBudget, adviceLineLimit),
				ToolTipText: view.advice, MinSize: dec.Size{Height: 64}},
			dec.TextLabel{Text: fitText(view.details, reasonLineBudget, detailLineLimit),
				ToolTipText: view.details, MinSize: dec.Size{Height: 80}},
			dec.TextLabel{Text: fitText(view.override, reasonLineBudget, overrideLineLimit),
				ToolTipText: view.override, MinSize: dec.Size{Height: 48}},
			dec.VSpacer{},
			dec.Composite{
				Layout: dec.HBox{MarginsZero: true, Spacing: 8},
				Children: []dec.Widget{
					dec.HSpacer{},
					dec.PushButton{AssignTo: &close, Text: scalingCloseText, OnClicked: func() { d.dialog.Cancel() }},
				},
			},
		},
	}.Create(owner)
	if err != nil {
		return false, fmt.Errorf("create scaling dialog: %w", err)
	}
	d.dialog.SetCancelButton(close)
	act.SetEnabled(view.enabled)

	d.dialog.Run()
	return d.requested, nil
}

// onScalingMenu opens the dialog and, if the user asked for the change there, runs it
// exactly as the old button did.
func (w *window) onScalingMenu() {
	if w.busy {
		return
	}
	snapshot := w.provider.Snapshot()
	w.syncConfigState()
	w.updateScalingAvailability(snapshot)

	requested, err := runScalingDialog(w.mw, w.scalingViewFor(snapshot))
	if err != nil {
		w.reportError(scalingApplyOperation, err, snapshot)
		return
	}
	if requested {
		w.onScaling()
	}
}
