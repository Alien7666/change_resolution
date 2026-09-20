# 已知缺陷（2026-09-19）

樹是綠的，HEAD `96e2ee4`。下列缺陷都**已確認、未修復**。

---

## 1. 規劃器不判斷共線 — 阻斷性

**症狀**：選非主螢幕、且與主螢幕不同排的螢幕當目標，套用時直接失敗：

```
plan display layout: display layout is not safe to apply:
\\.\DISPLAY1 and \\.\DISPLAY2 would overlap
```

**重現**：目標選 2461W（上排），套用 1440 × 1080 @ 60 Hz。

**原因**：`PlanModeChange` 的位移條件只比座標：

```go
if display.Position.X > target.Position.X {
    next.Position.X = display.Position.X - deltaX
}
```

XV272K 在 x=2560 > 目標的 2556，所以被左移 480 到 2080，撞進 Mi Monitor。但 XV272K 在 y=0 那排、目標在 y=−1080 那排，兩者根本不相干。

寫死 Mi 當目標時，Mi 是主螢幕在 (0,0)，所有東西都在它右邊或上方，這個缺陷永遠不會顯現。

**影響**：工具**安全地拒絕**，不會破壞桌面。但通用化的核心前提（目標可以是任何一台螢幕）在這個情況下失效。

**已定案的修法**：位移條件改成「起點在目標移動的那條邊之外，**且**在垂直方向與目標的帶狀範圍重疊量 > 0」。作者已選定此方案，接受它帶來的行為變更：Mi 切 4:3 時上排兩台會留在原地，不再跟著左移（桌面仍連續，滑鼠仍到得了每一台）。

**未完成的原因**：改動波及 11 個測試，其中三個是**安全拒絕**的測試，改動後不再拒絕：

- `TestPlanModeChangeRefusesLayoutsItCannotMakeSafe/displays_would_overlap`
- `TestValidateArrangementNamesBothDisplaysThatWouldOverlap`
- `TestEnableAbortsWhenALargerModeHasNoSafeArrangement`

推測是那些 fixture 當初就是為了讓舊位移規則製造重疊而設計的，新規則下該重疊不再發生，所以前提失效。**但這是推測，沒有逐一查證。** 「前提失效」與「安全防線被弄鬆」的差別很重要，弄錯的代價比這個缺陷本身大。

已寫好的實作存在 `scratchpad/planner-slide-rule.patch`（79 行），可直接套用作為起點。接手時必須逐一檢視那 11 個測試，對每一個明確判定是「預期的行為變更」還是「真的回歸」，不得整批改掉期望值。

---

## 2. 設定對話框：儲存被擋住卻不說原因 — 死路

**症狀**：螢幕、模式、程序都選好了，「儲存」仍是灰的，畫面上沒有任何說明。

**原因**：`settingsDialogFlow.Gates()`：

```go
ready, reason := f.model.SaveReady()
return settingsGates{
    Save:   ready && f.processChosen,
    Reason: reason,
}
```

`SaveReady()` 滿足時回傳空字串的 reason，所以只被 `processChosen` 擋住的草稿會得到 `Save == false` 且 `Reason == ""`。`applyGates` 的顯示條件是 `gates.Reason != ""`，於是什麼都不顯示。

真正的說明文字 `請選擇要觀察的程序，或明確選擇只用手動切換` 寫在 `onSave` 裡，而 `onSave` 掛在那顆被停用的按鈕上，永遠不會執行。

**修法**：讓 `Gates()` 成為決定 reason 的唯一地方，每個被擋住的閘門都帶著能解除它的那句話。另外 `applyGates` 只在標籤為空時才寫入，過期訊息會活得比條件久，也要一併處理。

---

## 3. 設定對話框：程序區塊沒反應

**症狀**：搜尋框打字沒有過濾效果；點清單裡的程序名稱，下方輸入框仍是空的（顯示灰色提示文字）。

**已排除**：`ProcessNames(query)` 的過濾邏輯本身正確（`strings.Contains` + 小寫）。螢幕與模式的選取是正常的 — 否則 `SaveReady()` 會回傳非空的 reason 並顯示出來。

**待查**：`rebuildProcesses` 呼叫 `SetCurrentIndex(indexOfStringFold(rows, draft.ProcessName))`，而首次設定時 `draft.ProcessName` 是空字串。**要確認 `indexOfStringFold` 對空字串回傳什麼** — 如果它匹配到第 0 列，清單看起來就會像已經選了東西，而實際上沒有，這與截圖完全吻合。

**測試蓋不到的原因**：UI 測試刻意做成不啟動 Walk 的純函式測試，跑得快、不需要訊息迴圈，但「控制項有沒有真的接上事件」完全測不到。gates 的邏輯測得很細，實際點下去沒反應卻沒人發現。

---

## 現況

- 情境 1、2、3 全部卡在缺陷 1 或 2/3
- 計畫 86/87 步，唯一未勾的是手動驗收（Task 17 Step 4）
- 手動測試文件：`docs/superpowers/verification/2026-09-19-manual-test-plan.md`
- `docs/superpowers/verification/2026-09-18-configurable-tray.md` 有未提交的變更（codex 的實測紀錄）
