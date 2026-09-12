# VALORANT 4:3 顯示工具

Windows 系統匣工具，用來手動切換 Mi Monitor（`MONITOR\XMI27B2`）的顯示模式，並在 VALORANT 關閉後自動恢復原始解析度。取代原本的 Python/PyInstaller 解析度切換腳本。

## 這個工具做什麼

- 只控制**指定的 Mi Monitor** 顯示模式，不動其他三台螢幕的解析度、刷新率或位置。
- 開啟「使用 4:3」時，套用 `1920×1440 @ 180 Hz`（32 bpp）；關閉時恢復啟用當下保存的原始模式。
- 4:3 啟用期間會觀察 `VALORANT-Win64-Shipping.exe` 是否還在 Windows 程序清單中；程序從「存在」變成「不存在」後，等待 **3 秒**再自動恢復。
- 只是**讀取**與**呼叫 Win32 顯示 API**，不會啟動、注入、掛勾 VALORANT，也不會開啟其行程、讀取其記憶體，或碰觸 Riot / Vanguard / 遊戲檔案。

## 事前準備：NVIDIA 全螢幕縮放

Mi Monitor 只有原生 `2560×1440`，切到 `1920×1440` 屬於非原生比例。為了讓畫面正確縮放並避免變形黑邊，需要**在顯示卡層級啟用縮放**（一次性設定）：

1. 開啟 NVIDIA 控制台 → **調整桌面尺寸與位置**。
2. 縮放模式選擇「全螢幕」，並將「執行縮放的裝置」設為 **GPU**（而非顯示器或顯示器內建縮放）。
3. 套用後即可。此設定只需做一次，之後每次工具切換解析度都會沿用。

## 使用者流程

1. **啟動工具不會改變任何螢幕。** 主視窗只讀取並顯示 Mi Monitor 目前的解析度與刷新率。
2. **手動切換 4:3：** 勾選「使用 4:3（1920×1440 @ 180 Hz）」後，工具會：
   - 以硬體 ID 前綴 `MONITOR\XMI27B2` 動態找出對應的 `\.\DISPLAYn`（不是寫死的 `DISPLAY1`）。
   - 保存該螢幕目前完整的顯示模式。
   - 先以 `CDS_TEST` 驗證目標模式，成功後才正式套用。
   - 視窗可隱藏至系統匣繼續執行。
3. **取消勾選 / 按「恢復 2K」：** 立即恢復先前保存的原始模式，並取消任何尚未執行的自動恢復計時。
   - 若啟動時螢幕本來就已是 4:3（工具沒有保存到原始模式），手動恢復會套用 profile 內建的 `FallbackNativeMode`（`2560×1440 @ 180 Hz`）。
4. **自動恢復：** 4:3 啟用期間，工具每秒檢查一次 `VALORANT-Win64-Shipping.exe` 是否還在程序清單中。程序從「有看到」變成「消失」的 3 秒後，自動恢復原始模式；若計時尚未到期遊戲又重新出現，恢復會被取消。
5. **系統匣行為：** 關閉或最小化主視窗只會隱藏至系統匣，工具持續執行；托盤選單提供「顯示主視窗」「使用 4:3」「恢復原始解析度」「結束」。左鍵點擊托盤圖示會還原並前景化主視窗。
6. **結束工具：** 若目前仍由本工具管理 4:3，會先嘗試恢復原始模式再結束；若恢復失敗，工具會回報錯誤並保持執行，不會默默放著 4:3 不管。

切換解析度當下螢幕會短暫黑屏一下，這是實體顯示模式切換無可避免的現象；工具的目標是讓黑屏只發生在「手動開啟」「手動關閉」「遊戲結束後自動恢復」這三個時間點，不會因為 VALORANT 內部的 Alt+Tab / 桌面模式切換而反覆觸發。

## 安全邊界（工具絕對不做的事）

- **絕不啟動、注入或掛勾 VALORANT**，也不會以任何方式和遊戲行程互動。
- **絕不開啟 VALORANT 的行程控制代碼（`OpenProcess`）或讀寫其記憶體**；唯一使用的 API 是 `CreateToolhelp32Snapshot` + `Process32First(W)` / `Process32Next(W)`，只列舉行程的**執行檔名稱**做字串比對。
- **絕不觸碰 Riot Client、Vanguard 或任何遊戲檔案**。
- **絕不使用 `CDS_UPDATEREGISTRY`**：解析度變更只是暫時性的執行期切換，不會寫回登錄檔／永久生效；意外結束工具也不會讓 4:3 變成使用者的永久設定。
- **絕不修改 Mi Monitor 以外的其他顯示器**：不改變它們的解析度、刷新率或桌面位置。
- 套用目標模式前一定先以 `CDS_TEST` 驗證，驗證失敗就不會正式套用。

## 建置

需求：Windows 10/11 amd64、Go 1.27.x（若未安裝於 PATH，`build.ps1` 會退回使用 `C:\Program Files\Go\bin\go.exe`）。

```powershell
# 完整可重現建置：go generate → go test ./... → go vet ./... → 建置 GUI 版 exe
./build.ps1
```

成功後會產生 `dist/ResolutionTray.exe`（GUI subsystem，不會顯示主控台視窗；`dist/` 已列入 `.gitignore`，不會提交進版控）。

### 個別指令

```powershell
# 只跑單元測試與安全的 Win32 結構測試（不會實際切換螢幕解析度）
go test ./...

# 選擇性整合測試：只執行 CDS_TEST 驗證，不會真的改變顯示模式
$env:RUN_DISPLAY_INTEGRATION = '1'
go test ./internal/display -run TestWindowsControllerCanTestMiMonitorMode -v
Remove-Item Env:RUN_DISPLAY_INTEGRATION
```

若 `go` 不在 PATH 上，可先執行：

```powershell
$env:PATH = "C:\Program Files\Go\bin;$env:PATH"
```
