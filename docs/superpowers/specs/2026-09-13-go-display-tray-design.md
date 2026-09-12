# Go Display Tray 重構設計

## 目標

將現有 Python 解析度切換腳本重構為 Windows-only 的 Go 系統匣工具。工具只控制指定的 Mi Monitor 顯示模式，不啟動、不注入、不掛勾 VALORANT，也不讀寫遊戲檔案；使用者可以隨時手動切換 4:3，並在遊戲關閉後自動恢復原始解析度。

## 使用者流程

1. 啟動工具時只讀取並顯示目前狀態，不變更任何螢幕。
2. 使用者開啟「使用 4:3」時：
   - 以硬體 ID `MONITOR\\XMI27B2` 找到 Mi Monitor 對應的 `\\\\.\\DISPLAYn`。
   - 保存該螢幕目前完整的顯示模式。
   - 驗證並套用 `1920×1440 @ 180 Hz`。
   - 將視窗隱藏至 Windows 系統匣。
3. 4:3 啟用期間，使用者可隨時關閉開關；工具會取消尚未執行的自動恢復計時，並立即恢復先前保存的模式。
4. 4:3 啟用後，工具只觀察 `VALORANT-Win64-Shipping.exe` 是否曾出現在 Windows 程序清單中。程序由存在轉為不存在時，等待 3 秒後恢復原始模式。
5. 退出工具時若仍由本工具啟用 4:3，先恢復原始模式再退出。

工具不負責啟動 VALORANT，也不會因偵測到遊戲啟動而切換解析度。若 4:3 啟用後遊戲從未出現，解析度維持不變，直到使用者手動關閉 4:3 或退出工具。

## 技術選擇

- Go 1.27.x，目標平台 `windows/amd64`。
- `golang.org/x/sys/windows` 呼叫 Windows API。
- `github.com/lxn/walk` 建立原生 Windows 視窗與通知區圖示。
- `ChangeDisplaySettingsExW` 明確指定顯示裝置。
- `CreateToolhelp32Snapshot` 與 `Process32FirstW` / `Process32NextW` 只列舉程序名稱。
- Windows GUI subsystem 建置，成品為單一 `.exe`，不顯示主控台視窗。

Walk 適合此 Windows-only 小工具，能提供原生控制項與系統匣功能，且不需要 Wails 的 WebView/前端工具鏈，也不需要 Fyne 的 CGO/C 編譯器。

## 元件與邊界

### 顯示控制

`DisplayController` 負責列舉顯示裝置、以硬體 ID 對應目標螢幕、讀取目前模式、驗證目標模式、套用模式及恢復模式。

第一版預設設定：

- 顯示器硬體 ID 前綴：`MONITOR\\XMI27B2`
- 4:3 模式：`1920×1440 @ 180 Hz`、32 bpp
- 原始模式：啟用 4:3 當下動態擷取，不硬編碼；只有在無法取得已保存模式時，GUI 才提供明確的 `2560×1440 @ 180 Hz` 緊急恢復動作。

套用前先以 `CDS_TEST` 測試模式；正式套用不使用 `CDS_UPDATEREGISTRY`，避免將暫時遊戲模式永久寫入使用者設定。除目標 Mi Monitor 外，不修改其他顯示器的解析度、刷新率或位置。

### 程序觀察

`ProcessWatcher` 每秒以 Toolhelp snapshot 列舉一次程序名稱，不取得 VALORANT 程序控制代碼、不讀取程序記憶體，也不與 Vanguard 或遊戲視窗互動。

只有在本工具成功啟用 4:3 後才建立觀察狀態：

- 尚未看見遊戲：不動作。
- 看見遊戲：記錄本次執行已開始，不變更顯示模式。
- 遊戲消失：啟動可取消的 3 秒恢復計時。
- 計時期間遊戲重新出現：取消恢復。
- 使用者手動關閉 4:3：取消觀察與計時，立即恢復。

### 應用程式狀態

核心狀態機與 GUI 分離，狀態包含：

- `Native`：未由工具套用 4:3。
- `Applying`：正在驗證並套用 4:3。
- `ActiveWaitingForGame`：4:3 已啟用，尚未觀察到遊戲。
- `ActiveGameRunning`：4:3 已啟用且遊戲正在執行。
- `RestorePending`：遊戲已關閉，等待 3 秒。
- `Restoring`：正在恢復。
- `Error`：操作失敗，保留可恢復的狀態與錯誤訊息。

所有耗時操作在背景 goroutine 執行；GUI 更新切回 Walk UI 執行緒。解析度切換操作序列化，避免快速重複點擊造成競爭。

### GUI 與系統匣

主視窗保持精簡：

- Mi Monitor 是否已找到。
- 目前解析度與刷新率。
- 「使用 4:3（1920×1440 @ 180 Hz）」開關。
- 目前狀態或錯誤訊息。
- 「隱藏至系統匣」與「恢復 2K」按鈕。

關閉主視窗只隱藏至系統匣。系統匣選單包含：

- 顯示主視窗。
- 使用 4:3。
- 恢復原始解析度。
- 結束。

若目標螢幕不存在或目標模式不受支援，停用 4:3 開關並顯示原因，不變更其他螢幕。

## 可擴充性

硬體設定放在 `DisplayProfile`：

```text
Name
MonitorHardwareID
GameMode
FallbackNativeMode
ProcessName
RestoreDelay
```

第一版只內建一個 Mi Monitor profile，但顯示控制與 GUI 不依賴固定的 `DISPLAY1`。未來加入螢幕與解析度選擇器時，只需提供多個 profile 或設定儲存，不更動 Win32 顯示控制與核心狀態機。

## 錯誤與恢復

- 找不到螢幕：保持現況並顯示錯誤。
- 目標模式測試失敗：不套用模式。
- 套用失敗：顯示 Win32 錯誤／`DISP_CHANGE_*` 結果。
- 恢復失敗：保留「恢復 2K」動作，並在系統匣顯示錯誤通知。
- 工具正常退出：盡力恢復由本次執行保存的原始模式。
- 工具或系統非正常終止：無法保證執行清理；因不使用 `CDS_UPDATEREGISTRY`，不永久寫入暫時模式。再次開啟工具時不自動改變顯示模式，以符合「啟動不做任何動作」要求。

程式不承諾顯示模式實際切換時零黑屏；目標是讓切換只發生在手動啟用、手動關閉或遊戲結束後恢復三個時點，並避免 VALORANT Alt+Tab 觸發不同桌面模式的往返切換。

## 測試策略

- 狀態機使用假的 DisplayController、ProcessWatcher 與可控制時間的 timer 做單元測試。
- 驗證手動開啟、手動關閉、遊戲從未出現、遊戲結束延遲恢復、延遲期間重開遊戲、退出恢復與錯誤路徑。
- Win32 結構轉換與程序名稱比對使用純函式測試。
- Windows integration test 只做 `CDS_TEST` 與唯讀列舉，預設測試不得實際改變使用者解析度。
- 發布前人工 smoke test 才在 Mi Monitor 上實際切換並確認另外三台螢幕未被修改。

## 專案整理

Go 版本達成功能對等並完成驗證後，刪除舊 Python 腳本、PyInstaller spec、已提交的 `build/` 與 `dist/` 產物，以及本次工作區內對應的未追蹤舊產物。加入 `.gitignore`，避免再次提交編譯輸出。

不修改使用者電腦上的 VALORANT 設定、Riot Client、Vanguard 或 NVIDIA 控制台設定。
