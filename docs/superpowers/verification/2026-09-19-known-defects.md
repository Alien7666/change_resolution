# 已知缺陷（2026-09-19 起；缺陷 1 已於 bfc688d、2 與 3 於 6c54796、4 於 1f39bf7 修復）

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

## 2. 設定對話框：儲存被擋住卻不說原因 — **已修復 `6c54796`**

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

## 3. 設定對話框：程序區塊沒反應 — **已修復 `6c54796`**

**症狀**：搜尋框打字沒有過濾效果；點清單裡的程序名稱，下方輸入框仍是空的（顯示灰色提示文字）。

**已排除**：`ProcessNames(query)` 的過濾邏輯本身正確（`strings.Contains` + 小寫）。螢幕與模式的選取是正常的 — 否則 `SaveReady()` 會回傳非空的 reason 並顯示出來。

**待查**：`rebuildProcesses` 呼叫 `SetCurrentIndex(indexOfStringFold(rows, draft.ProcessName))`，而首次設定時 `draft.ProcessName` 是空字串。**要確認 `indexOfStringFold` 對空字串回傳什麼** — 如果它匹配到第 0 列，清單看起來就會像已經選了東西，而實際上沒有，這與截圖完全吻合。

**測試蓋不到的原因**：UI 測試刻意做成不啟動 Walk 的純函式測試，跑得快、不需要訊息迴圈，但「控制項有沒有真的接上事件」完全測不到。gates 的邏輯測得很細，實際點下去沒反應卻沒人發現。

---

## 缺陷 2、3 的修復紀錄（6c54796）

兩者是同一個病灶的兩面：**控制項被停用，但畫面不解釋。**

缺陷 3 的根因不是清單或搜尋壞掉 — 模型層與流程層的測試證明過濾、選取、草稿寫入全部正確。真正的原因是 `applyGates` 的 `d.processGroup.SetEnabled(gates.Process)`：沒選模式時整個容器被停用，Walk 會讓裡面每一個控制項靜默忽略輸入，而畫面上沒有任何跡象。使用者看到的是「清單點不動」，實際上是「上一步還沒完成」。

修法：

- `Gates()` 成為決定 reason 的唯一來源，被 `processChosen` 擋住時補上對應句子；`Save()` 不再自己留一份複本
- 狀態列改成跟著閘門走，並用 `isGateReason` 區分「閘門說明」與「別處來的訊息」（例如儲存失敗），避免清掉使用者還需要看的內容
- 每個被停用的區塊自己說明解鎖條件，顯示在拒絕輸入的那些控制項旁邊，而不只在底部狀態列

驗證：`TestBlockedSaveAlwaysCarriesTheSentenceThatUnblocksIt` 經突變測試確認會咬 — 拿掉修正即失敗，訊息正是空字串。

---

## 現況

- 情境 1、2、3 全部卡在缺陷 1 或 2/3
- 計畫 86/87 步，唯一未勾的是手動驗收（Task 17 Step 4）
- 手動測試文件：`docs/superpowers/verification/2026-09-19-manual-test-plan.md`
- `docs/superpowers/verification/2026-09-18-configurable-tray.md` 有未提交的變更（codex 的實測紀錄）

---

# 2026-09-20 追加

缺陷 1 已於 `bfc688d` 修復（11 個測試逐一判定，新增兩條迴歸測試並經突變驗證）。實機重測情境一時又發現兩個。

## 4. 螢幕被重新編號後沒有回頭路 — 阻斷性 — **已修復 `1f39bf7`**

**症狀**：工具正在管理模式時，取消勾選解析度、按「恢復原始解析度」、關閉程式，三者都撞同一個錯：

```
plan display layout: display layout is not safe to apply:
the configured monitor moved from \.\DISPLAY8 to \.\DISPLAY5 while its mode was managed
```

`onExit` 在 `Shutdown()` 失敗時不呼叫 `finishExit()`（這是刻意的：還原失敗不該默默放棄使用者的模式），於是**視窗關不掉**，只能從工作管理員結束行程，再手動把螢幕設回去。

**重現**：2461W 設為目標並套用 1440×1080，按 GPU 縮放按鈕。NVAPI 寫入把 2461W 從 `\.\DISPLAY8` 換成 `\.\DISPLAY5`。

**原因**：`restoreSaved` 拿存下來的 `\.\DISPLAYn` 跟現在解析出來的比對，不同就放棄。但 DISPLAYn 是插槽不是螢幕，而**工具自己的 NVAPI 寫入正是會搬動插槽的東西之一** — R1–R5 排序規則整套就是為了這件事設計的，卻沒有人處理「已經被換號之後怎麼辦」。

**修法**：`remapSavedNames` 用 `captureDisplayBindings` 早就存在旁邊的螢幕身分，把存下來的名字重新綁到現在的名字上，讓座標跟著擁有它的螢幕走。仍然要成立的條件是「每一台存過的螢幕都還接著」，至於它現在叫幾號不關工具的事。真的被拔掉仍然拒絕，而且訊息會點名是哪一台（檢查順序特意排在數量比對之前，否則只會說「4 台變 3 台」）。

`verifyDisplayBindings` 一併移除：它的兩項檢查正是重新對應在做的事，差別只在那個「名字必須相同」的要求 —— 那就是缺陷本身。

**測試判定**：三個測試受影響，逐一處理，沒有整批改期望值。

- `TestRestoreAbortsWhenTheSavedArrangementNoLongerFitsTheDesktop`、`TestRestoreRefusesWhenTheResolvedTargetNowUsesAnotherSavedDeviceName`：只有呼叫順序期望要改（現在必須先讀桌面與身分才判斷名字）。兩者仍然拒絕，理由不變
- `TestRestoreNamesTheNeighbourThatIsNoLongerInTheSavedLayout`：它用的 `renameDisplay` 同時改 layout 和 targets，模擬的是**重新編號**而不是**拔掉**。新程式分得出兩者，所以這個測試現在斷言的是缺陷本身。拆成兩條：重新編號 → 還原成功；真的拔掉（新增 `detachDisplay`）→ 拒絕並點名
- `TestRestoreRefusesWhenNeighboursExchangeSavedDeviceNames` 沒有失敗，但通過的理由變了 — 現在是重新對應後的排列真的重疊，由排列器擋下（`\.\DISPLAY4 and \.\DISPLAY2 would overlap`）。已實測確認並改寫註解，不留「碰巧通過」

新增 `TestRestoreFollowsTheConfiguredMonitorThroughARenumbering`，就是使用者遇到的那一條。

**突變驗證**：

- 拿掉 layout 改名 → 三條重新編號測試全倒
- 拿掉 `managedTargetDevice` 更新 → 目標改號那條倒，錯誤正是原本的 `moved from ... while its mode was managed`
- 把「不見了」改成沿用舊名 → 拔掉那條倒，訊息退化成「4 台變 3 台」，點不出是哪一台

重複身分那個防護測不到 — 上游 `captureDisplayBindings` 已經擋掉重複。保留為不變量守衛，程式與測試都標註它不可達，不假裝有覆蓋。

## 5. GPU 縮放要到全螢幕卻拿到長寬比 — 未修復

**症狀**：套用 1440×1080 後按 GPU 縮放，視窗顯示：

> 已要求「全螢幕（由 GPU 執行）」，驅動實際套用的是「長寬比（由 GPU 執行）」

黑邊不消失。

**已確認的事實**（使用者實機讀數）：

| 讀數 | 結論 |
|---|---|
| 管理循環寫入後，在 1440×1080 讀到 raw 5 | 寫入有生效也跨模式存活（原本 raw 6 → 變 5 並留住）。不是暫時性寫入 |
| NVIDIA 控制台手動設 raw 2，黑邊消失 | 驅動在 1440×1080 下接受 raw 2，而且 2 真的能消黑邊 |
| 程式讀回 raw 2 | 讀取端正確，`decodeValue` 對照表無誤 |

所以唯一壞掉的是「要 2、拿到 5」。`flagValidateOnly` 那一趟先回 `statusOK`，代表 2 是合法值而非被拒絕。

**假說**：`runScalingCycle` 步驟 2 先 `restoreSaved()` 把螢幕還原成原生 2560×1440，步驟 3 才寫入 NVAPI。原生下 source == native，`NV_SCALING_GPU_SCALING_TO_NATIVE`(2) 是空操作，驅動改存 5。步驟 4 才切回 1440×1080。

**未證實**。原本設計的測試因為起始值已經是 2，`applyLocked` 的同值 no-op 直接跳過，沒發出任何 NVAPI 呼叫，所以測不到。正確的測法是先把 NVCP 設成外觀比例（raw 5）造出不同的起始值，再讓程式在 1440×1080 下走 `writeScaling`（未管理路徑）。

**若假說成立**，修法要把 NVAPI 寫入移到套用遊戲模式之後，而那會讓裝置名稱在持有排列時被重新編號 — 正是缺陷 4 修好的那套重新對應機制要派上用場的地方。

**附帶**：視窗那句「請到 NVIDIA 控制台勾選『覆寫遊戲和程式所設定的縮放模式』」對桌面黑邊是錯的，那個勾選管的是遊戲覆寫縮放。根因確定後一併改掉。
