# GPU 縮放控制設計（NVIDIA）

## 目標

把顯示卡層級的「全螢幕縮放」從一個使用者要自己到 NVIDIA 控制台做的前置步驟，變成工具裡的一顆按鈕。

工具切到 4:3（例如 16:9 面板上的 `1920×1440`）時，若顯示卡的縮放模式不是「全螢幕 + 由 GPU 執行」，畫面會被加上黑邊而不是拉滿。目前 README 把這件事寫成一次性手動設定（`README.md`「事前準備：NVIDIA 全螢幕縮放」），而 `2026-09-13-configurable-profile-design.md` 把它明確列在不在範圍內，只承諾「挑到非原生比例時提醒你有這件事」。本設計就是那份提醒的另一半：讓工具真的能讀、也能寫這個值。

**只做 NVIDIA。** AMD 與 Intel 各需要完全不同的 DLL、不同的 id 機制與不同的裝置定址方式（ADL 的 `atiadlxx.dll`、Intel 的 igcl），沒有任何共用面，而且這台開發機上沒有可以驗證的硬體。偵測到非 NVIDIA 就停用按鈕並指路到該廠商的控制台，不假裝支援沒測過的硬體。

`2026-09-13-go-display-tray-design.md` 與 `2026-09-13-configurable-profile-design.md` 的所有承諾在本文件中都是前提，一字不變：

- 啟動只讀取與顯示狀態，永遠不主動變更任何顯示模式，**也不主動變更任何縮放設定**。
- 永遠不使用 `CDS_UPDATEREGISTRY`；套用任何模式之前一定先做 `CDS_TEST`。
- 不取得遊戲程序控制代碼，不啟動、不注入、不掛勾遊戲，不碰 Riot / Vanguard / 遊戲檔案。
- 其他顯示器的解析度、刷新率與色彩深度永遠不在寫入集合內；主要顯示器固定在 `(0,0)`。
- 由本工具套用的模式，在手動關閉、被觀察的程序結束後、以及工具退出時都會恢復。

本設計為這串加上一條同型的約束：**永遠不送出 `NV_DISPLAYCONFIG_SAVE_TO_PERSISTENCE`。** 它是 `CDS_UPDATEREGISTRY` 在 NVAPI 世界的對應物，理由完全相同。

## 不在範圍內

- **「覆寫遊戲和程式所設定的縮放模式」這個核取方塊。** 它沒有任何 NVAPI 介面：不在 `NV_SCALING`、不在 `NV_DISPLAYCONFIG_PATH_ADVANCED_TARGET_INFO`、也不在 `NvApiDriverSettings.h`（已逐項檢查，該檔只有 NGX/DLSS 的 upscaling 設定）。**這一項使用者仍然要自己到 NVIDIA 控制台勾一次。** 工具不會假裝它做得到，也不會宣稱「已設定好」——見「讀回與 UI 呈現」對誠實邊界的說明。
- AMD 與 Intel。
- 旋轉、刷新率、色彩格式、timing override、`preferredUnscaled` 等同一結構裡的其他欄位。它們會被原封不動地寫回去，但工具不提供修改它們的方式。
- 自訂解析度（`NvAPI_DISP_TryCustomDisplay` / `SaveCustomDisplay`）。`SaveCustomDisplay` 明確是持久化的，與本專案的核心約束直接衝突；理由在 profile 設計的「後續階段：自訂（驅動未列舉的）解析度」已經寫過。
- 對目標以外的顯示器變更縮放。工具只會改一台，而且是使用者設定中的那一台。
- 持久化。工具寫的是 runtime-only 的值，重開機或驅動重載之後就回到使用者在 NVIDIA 控制台裡的設定。

## 這份設計站在什麼實測結果上

以下每一條都是在這台機器（RTX 3070 + 第二張 GPU、四螢幕、驅動 32.0.16.1074）上量到的，不是推論。

- 介面 id 全部來自 NVIDIA 官方的 `nvapi_interface.h`（`github.com/NVIDIA/nvapi`），不是部落格上流傳的表。用到的是 `NvAPI_Initialize` `0x0150E828`、`NvAPI_DISP_GetDisplayConfig` `0x11ABCCF8`、`NvAPI_DISP_SetDisplayConfig` `0x5D8CF8DE`、`NvAPI_DISP_GetDisplayIdByDisplayName` `0xAE457190`、`NvAPI_GetErrorMessage` `0x6C2D048C`、`NvAPI_Unload` `0xD22BDD7E`。全部經由 `nvapi64.dll` 的 `nvapi_QueryInterface` 取得，不需要 cgo。
- x64 結構版本：`NV_DISPLAYCONFIG_PATH_INFO_VER2` = `0x00020030`、`NV_DISPLAYCONFIG_PATH_ADVANCED_TARGET_INFO_VER1` = `0x00010080`。版本錯了會拿到 `NVAPI_INCOMPATIBLE_STRUCT_VERSION (-9)`，這一點用故意寫錯的負向控制驗證過（`0x00090030` 與 `0x00020028` 都被拒絕），所以「版本是對的」不是「沒報錯所以大概對」。
- `NV_SCALING` 一個欄位同時編碼了控制台上的兩個下拉：縮放模式（全螢幕／長寬比／無縮放／整數）與執行縮放的裝置（`_TO_NATIVE` → GPU「Force GPU」、`_TO_CLOSEST` → 顯示器「Balanced」）。**本功能要的值是 `2 = ForceGPU-FullScreen`。** `1` 是 Balanced-FullScreen，`6` 是 Balanced-AspectRatio，也是這台機器上 NVIDIA 的預設。
- Windows CCD 的 `DISPLAYCONFIG_SCALING` **不是替代方案**。在同一個時刻查詢，四台顯示器的 CCD 值全部是 `IDENTITY (1)`，而 NVAPI 同時回報 `2 / 6 / 6 / 6`。Windows 看不到這個設定，自然也改不了它。
- **持久化已定案。** `NV_DISPLAYCONFIG_SAVE_TO_PERSISTENCE`（`0x02`）是 opt-in。一次受監督的實驗在不帶該旗標的情況下寫入了縮放值，並在前後比對 `HKLM\SYSTEM\CurrentControlSet\Services\nvlddmkm\State\DisplayDatabase`：SHA256 相同、388 行、零差異。不帶旗標的寫入就是 runtime-only，與本專案拒送 `CDS_UPDATEREGISTRY` 的模型一致。

另外兩個實測行為不只是註腳，它們直接決定了本設計的形狀，各有專節。

---

## 實測行為一：驅動不一定會存下你寫的值

第 2 階段對 Mi Monitor 寫入 `6`。`VALIDATE_ONLY` 回 `NVAPI_OK`，正式套用也回 `NVAPI_OK`，同一個行程內立刻重讀，拿到的是 **`1`**，而且沒有任何警告。

最可能的原因是正規化：當下桌面在面板原生的 `2560×1440`，此時「長寬比」與「全螢幕」是同一件事，驅動把模式那一半收斂掉，只留下裝置那一半（`_TO_CLOSEST` → Balanced）。本功能真正要用的值 `2` 則忠實地往返成功（第 3 階段：寫 `2`、讀回 `2`）。

設計後果，寫成規則：

> **永遠不相信自己寫下去的值。每一次寫入之後都重新讀，UI 顯示的永遠是讀回來的值，不是要求的值。**

這不是防禦性程式碼的客套話，而是 UI 的單一真相來源：畫面上那行「GPU 縮放：…」的字串只有一個來源，就是最後一次成功的讀取。工具沒有任何路徑會把「我剛才寫了 2」渲染成「現在是 2」。要求值與生效值不同時的呈現方式見「讀回與 UI 呈現」。

---

## 實測行為二：一次寫入會讓 `\\.\DISPLAYn` 重新編號，而且跨 GPU

第 1 階段是對照組：把**完全沒有修改**的設定原值寫回去（payload 與剛讀到的設定逐欄比對，0 個欄位不同）。`VALIDATE_ONLY` 與正式套用都回 `NVAPI_OK`，NVAPI 這一側的四台顯示器狀態一個位元都沒變。但 CCD 那一側：

```text
- \\.\DISPLAY5   2461W   adapter=00016EAC:0 targetId=45312
+ \\.\DISPLAY8   2461W   adapter=00016EAC:0 targetId=45312
```

那台 AOC 顯示器**在第二張 GPU 上**（displayId `0x82061083`、`gpuHandle=0xB00`），而被寫入的 path 屬於第一張 GPU（`gpuHandle=0xA00`）。`NvAPI_DISP_SetDisplayConfig` 是整台機器的全域操作，它的副作用也是全域的。`targetId` 全程穩定在 45312，`displayId` 也沒變；動的只有 GDI 名稱。

第 2、3 階段的兩次真實寫入**沒有**再改編號。所以這不是「每次都會」，而是「隨時可能」——一個非決定性的副作用，只能當成「任何一次 set 都可能發生」來設計。

這是兩個實測行為裡危險的那一個。`ChangeDisplaySettingsExW` 與 `EnumDisplaySettingsW` 定址的正是 `\\.\DISPLAYn` 這個字串，而 `internal/display` 解析出一個帶著 `DeviceName` 的 `domain.Target` 之後就一路拿它去用。任何跨過一次 NVAPI set 的裝置名稱都可能已經失效，而拿失效的名稱去動作，意思就是**改到別台螢幕**。

「記得要重新解析」是一句叮嚀，不是一條規則。本設計要求的是讓失效名稱在結構上無法存在，寫在「順序規則」一節。

---

## 供應商偵測與按鈕行為

偵測分成兩層，而且**判斷依據是功能性的，不是字串比對**。

1. **`nvapi64.dll` 能不能載入、進入點能不能解析、`NvAPI_Initialize` 能不能成功。** 任何一步失敗 → 這台機器上這個功能不存在。
2. **目標顯示器能不能對應到一個 NVIDIA displayId。** 即使 NVAPI 可用（機器上有 NVIDIA 卡），使用者設定的那台螢幕也可能掛在 Intel 內顯上。以 `NvAPI_DISP_GetDisplayIdByDisplayName` 對應不到，或對應到的 id 不在 `GetDisplayConfig` 回報的任何一條 path 上 → 這台螢幕不歸 NVIDIA 管。

廠商名稱字串（`EnumDisplayDevicesW` 回報的 adapter `DeviceString`，例如「NVIDIA GeForce RTX 3070」「AMD Radeon RX 7800 XT」「Intel(R) UHD Graphics」）**只用來寫訊息，永遠不參與判斷**。一張名字裡有 NVIDIA 的卡如果載不到 DLL 就是不可用；判斷永遠由第 1、2 層負責。

按鈕的狀態：

| 情況 | 按鈕 | 訊息 |
|---|---|---|
| NVAPI 可用且目標對得到 displayId | 啟用 | 顯示目前生效的縮放設定 |
| `nvapi64.dll` 不存在 | 停用 | 「偵測到 `<adapter DeviceString>`，這個功能只支援 NVIDIA。請到該顯示卡的控制台手動設定全螢幕縮放。」 |
| DLL 在但 `nvapi_QueryInterface` 或進入點解析不到 | 停用 | 「這個 NVIDIA 驅動版本不提供需要的介面。」 |
| `NvAPI_Initialize` 非 OK | 停用 | 帶出 NVAPI 狀態名稱與 `NvAPI_GetErrorMessage` 文字。 |
| NVAPI 可用但目標螢幕不在任何 NVIDIA path 上 | 停用 | 「`<螢幕名稱>` 不是由 NVIDIA 顯示卡驅動，這個功能不適用。」 |
| 目標螢幕目前找不到（睡眠、切輸入源） | 停用 | 沿用既有的 `ErrTargetNotFound` 文案；「重新整理」是出路。 |

**停用永遠附帶原因，而且原因寫在按鈕旁邊，不是藏在 tooltip 裡。** 這與既有 spec 對「目標模式不受支援」的處理一致。

**DLL 載入方式是安全性要求，不是風格選擇。** `nvapi64.dll` 由驅動安裝在 `System32`，因此一律用 `windows.NewLazySystemDLL`（`LOAD_LIBRARY_SEARCH_SYSTEM32`），與 `internal/display/win32_windows.go` 載入 `user32.dll` 的方式相同。本工具是使用者直接丟在 `Downloads` 執行的單一免安裝執行檔，那正是 DLL planting 最典型的場景；用預設搜尋順序去載一個廠商 DLL，等於讓同目錄下的同名檔案取得執行權。

---

## 套件邊界

新增 `internal/scaling`，與 `internal/display` **平行**，不是它的下層也不是它的上層。

```text
internal/display   ChangeDisplaySettingsExW / EnumDisplaySettingsW / 排列規劃
internal/scaling   nvapi64.dll / NV_SCALING                          <- 新增
internal/app       Session：唯一同時持有兩者、並負責排序的地方
```

**`internal/display` 不得 import `internal/scaling`，反之亦然。** 兩者都不知道對方存在。需要協調的地方只有一處——順序——而順序由 `internal/app.Session` 決定，因為 `Session.opMu` 已經是整個程式裡序列化所有顯示工作流程的那一把鎖。把 NVAPI 放進 `display` 會讓那把鎖失去它現在清楚的意義；把它放進一個自己帶鎖的獨立物件，則會讓「哪一把鎖先拿」重新變成一個沒有答案的問題。

檔案組織照既有慣例：純邏輯放沒有 build tag 的檔案，syscall 放 `_windows.go`，中間夾一個介面縫讓測試用假實作。這正是 `internal/display` 的 `controller.go` / `win32_windows.go` 加上 `nativeAPI` / `win32API` 兩層縫的形狀。

```text
internal/scaling/controller.go          純邏輯：目標選取、單欄位變更、差異檢查、
                                        validate-then-apply、讀回比對。全平台可編譯、可測。
internal/scaling/controller_test.go     假 nvapi 驅動上述全部。
internal/scaling/nvapi_windows.go       DLL 載入、QueryInterface、三段式配置、syscall。
internal/scaling/nvapi_windows_test.go  結構大小與版本常數斷言（純 Go layout，CI 跑得動）。
```

型別草圖（示意，不是實作）：

```go
// Value 把 NV_SCALING 那一個欄位攤成它實際編碼的兩個維度。
// Raw 一律保留，因為 UI 要能誠實印出驅動回報的數字。
type Value struct {
	Raw  uint32
	Mode Mode // FullScreen / AspectRatio / NoScaling / IntegerScaling / Default / Customized
	By   By   // GPU（Force GPU）/ Display（Balanced）
}

// State 是「剛剛讀回來的事實」。DisplayID 是 NVAPI 的穩定鍵，
// 不是 \\.\DISPLAYn——後者會在任何一次 set 之後改變。
type State struct {
	DisplayID uint32
	Effective Value
}

type Outcome struct {
	Requested Value
	State     State
	Matched   bool // State.Effective.Raw == Requested.Raw
}

type Controller interface {
	Probe() Availability
	Read(domain.MonitorIdentity) (State, error)
	Apply(domain.MonitorIdentity, Value) (Outcome, error)
	Close() error
}
```

`Controller` 的簽章刻意**不收也不回任何 `domain.Target`**。這是順序規則的結構性基礎，理由在下一節。`domain.MonitorIdentity` 是 profile 設計定義的穩定身分（裝置介面路徑為主鍵、硬體 ID 為次鍵），與 `\\.\DISPLAYn` 無關。

內層縫：

```go
type nvapi interface {
	initialize() status
	readConfig() (*config, status)             // 內含三段式配置與版本戳記
	writeConfig(cfg *config, flags uint32) status
	displayIDByName(gdiName string) (uint32, status)
	errorMessage(status) string
	unload() status
}
```

**縫切在這裡，是因為線上面的每一件事都有判斷，線下面的每一件事都沒有。** 目標選取、單欄位變更、差異檢查、validate 先於 apply、旗標字、讀回比對——全部在上面，全部可以用假 `nvapi` 測。三段式的計數／配置／指標綁定在下面，它是純粹的記帳，沒有分支可以測錯；換來的代價是那段程式碼只有真硬體跑得到，這一點在「測試策略」誠實列出。

`config` 是一個持有所有 backing slice 的 Go 結構，`readConfig` 填好它、`writeConfig` 直接吃它。假實作因此可以直接檢查與竄改 payload，不必碰 `unsafe.Pointer`。

---

## 讀取路徑

讀是完全安全的，任何時候都可以做，不會改變任何狀態。因此它可以在啟動時跑、可以在「重新整理」時跑，完全符合「啟動不改變任何東西」。

1. **初始化一次。** 第一次使用時載入 DLL、解析進入點、`NvAPI_Initialize`，之後整個行程共用。不做每次呼叫都 init/unload 的來回。`NvAPI_Unload` 只在 `Close()` 呼叫一次。
2. **`NvAPI_DISP_GetDisplayConfig` 三段式：**
   - pass 1：`(&count, nil)` 取得 path 數量。
   - pass 2：配置 `count` 個 `NV_DISPLAYCONFIG_PATH_INFO`，**每一個都戳上 `0x00020030`**，再呼叫一次取得每條 path 的 `targetInfoCount`。
   - pass 3：依 `targetInfoCount` 配置 target 與 advanced-target 陣列，**每個 advanced-target 戳上 `0x00010080`**，把 `targetInfo` / `sourceModeInfo` 指標綁上 path，**再戳一次 path 的版本**（pass 2 會把它覆寫掉），最後呼叫取得完整設定。
   - 每一輪都重新清零並重新戳版本，理由與 `loadCurrentMode` 每次重設 `DmSize` 的理由相同：不假設驅動保留了呼叫者給的值。
3. **身分對應。** 以 profile 設計的身分階梯解析出目標的 `\\.\DISPLAYn`，交給 `NvAPI_DISP_GetDisplayIdByDisplayName` 換成 displayId。這個 API 收的是 **ANSI byte string**（`\\.\DISPLAY1` 加一個 NUL），不是 UTF-16——這是整個 repo 裡唯一一個非 UTF-16 的 Win32 風格字串邊界，值得在程式碼旁邊寫一行註解。
4. **displayId 快取。** 對應成功之後快取 `MonitorIdentity → displayId`。**每次使用前都驗證：剛讀到的設定裡必須恰好有一個 target 帶著這個 displayId**，不符就丟掉快取、重跑第 3 步。這讓後續的縮放操作完全不需要再碰 GDI 名稱。
5. **恰好一個。** 在設定裡找 `displayId` 相符的 `(path, target)`。找到 0 個 → `ErrScalingTargetNotFound`；找到 2 個以上 → `ErrScalingTargetAmbiguous`。**永遠不取第一個。** 這條規則與 profile 設計對 `ErrTargetAmbiguous` 的處理同源。
6. 回傳 `State{DisplayID, Effective}`，`Effective` 由該 target 的 `details.Scaling` 解碼而來。

GC 相關的一條硬性要求：`NV_DISPLAYCONFIG_PATH_INFO` 裡的 `targetInfo` / `sourceModeInfo` 是**存在結構體裡的原始指標**，Go 的 GC 不會把它們當成參照。呼叫前後必須對每一個 backing slice 下 `runtime.KeepAlive`。這是本 repo 沒有過的危險類別——`internal/display` 只傳 `*devMode`，指標從不落進結構欄位裡。`go vet` 的 unsafeptr 檢查在這裡幫不上忙，所以它必須是 code review 的固定檢查項。

---

## 寫入路徑

整條路徑只有一個進入點，而且它是 `internal/scaling` 裡唯一會呼叫 `NvAPI_DISP_SetDisplayConfig` 的地方。

1. **在 `Session.opMu` 之下。** 縮放寫入與顯示模式寫入共用同一把操作鎖，兩者永遠不會交錯。
2. **重新讀一次完整設定。** 絕不重用先前讀到的 `config`。使用者可能在中間插了螢幕、換了輸入源，或是上一次 set 已經動過編號。
3. **選出恰好一個 target**（同讀取路徑第 5 步）。0 個或多個 → 拒絕，不寫。
4. **只改一個 `uint32`。** 在剛讀到的 payload 上把 `details[i][j].Scaling` 設成要求值。其他每一個欄位——rotation、refreshRate1K、flags、connector、timing、source mode、position——原封不動寫回去。工具不合成任何值。
5. **payload 對照剛讀到的設定做差異檢查。** 變更前先把 `config` 渲染成一組決定性的字串（指標值一律降維成 `nil` / `non-nil`，避免假差異），變更後再渲染一次，兩者的差異**必須恰好是那一行 scaling 欄位，不多不少**。任何其他差異 → `ErrScalingPayloadDiverged`，中止，不寫。
   這看起來像是在檢查自己剛剛寫的程式碼，而它確實就是——**而這正是它的價值**。這個檢查唯一會抓到的東西，是結構佈局與驅動不再相符（欄位偏移飄了、某個 padding 假設壞了），也就是那種會在 `SetDisplayConfig` 裡把整張桌面寫壞的 bug。它在驅動看到 payload 之前抓到它。實驗版的守衛就是這一段，它必須跟著出貨。
6. **`VALIDATE_ONLY` 前置驗證。** `NvAPI_DISP_SetDisplayConfig(count, paths, 0x01)`。任何非 `NVAPI_OK` → 中止，不做正式套用，回報 NVAPI 狀態名稱與 `NvAPI_GetErrorMessage` 文字。這是 `CDS_TEST` 在 NVAPI 這一側的同位物，地位相同：**沒有前置驗證就不套用**。
7. **正式套用，旗標字恰好為 `0`。** 不是「不含 `0x02`」，是恰好 `0`：`DRIVER_RELOAD_ALLOWED`（`0x04`）、`FORCE_MODE_ENUMERATION`（`0x08`）、`FORCE_COMMIT_VIDPN`（`0x10`）一律不送。
8. **重新讀一次，回報生效值。** 見「實測行為一」。
9. **釋放：** `runtime.KeepAlive` 涵蓋整個 `config` 的每一個 slice，直到 syscall 回傳之後。

### 持久化旗標的守衛

`SAVE_TO_PERSISTENCE` 這個常數會被宣告出來，**唯一的用途是讓守衛叫得出它的名字**：

```go
const (
	flagValidateOnly      uint32 = 0x01
	flagSaveToPersistence uint32 = 0x02 // 永遠不進入 flags；宣告只為了讓 guard 指名它
)

// writeConfig 之前唯一的關卡：旗標字只能是 0x00 或 0x01。
func checkFlags(flags uint32) error { /* 其餘位元一律拒絕 */ }
```

這與 `internal/display/win32_windows.go` 頂端那段「`CDS_UPDATEREGISTRY` 刻意不在這個檔案裡」的註解是同一個手法，而且既有測試 `TestWindowsFlagsMatchWin32AndExcludeUpdateRegistry` 就是新測試的樣板。

---

## 順序規則：讓失效的裝置名稱在結構上不存在

實測行為二的危害是：**任何跨過一次 NVAPI set 的 `\\.\DISPLAYn` 都可能指到別台螢幕。** 這條規則不是「記得重新解析」，它由四件事一起構成，而且最後一件把它從慣例升級成強制。

### R1 — 進場：先縮放，再模式

一次啟用 4:3 的工作流程，順序固定：

1. 取得 `opMu`。
2. **NVAPI 階段**：解析身分 → displayId → 讀縮放 → （若需要）寫縮放 → 讀回。
3. **然後才**是 `ResolveTarget` / `CurrentLayout`——顯示層對整張桌面的認知，完全建立在最後一次 set **之後**。
4. `CDS_TEST` → `ApplyLayout` → `verifyApplied`。

所以顯示模式永遠是對著重新編號**之後**的桌面規劃的。第 2 步之前算出來的任何名稱，都沒有活到第 3 步。

第 2 步自己也需要一個 GDI 名稱（`GetDisplayIdByDisplayName` 只收這個）。關鍵在於：**那個名稱在同一個函式裡被消費成 displayId 之後就被丟棄，從不離開 `internal/scaling`。** 第 3 步做的是一次完全獨立的、全新的 `ResolveTarget`。兩次解析，第二次嚴格在最後一次 set 之後。餵給 `ChangeDisplaySettingsExW` 的名稱，永遠不是餵給 NVAPI 的那一個。

### R2 — 離場：先模式，再縮放

恢復是進場的鏡像：

1. 取得 `opMu`。
2. **顯示恢復**：`PlanRestore(s.saved, ...)` → `ApplyLayout` → `verifyApplied`。
3. **然後才**是縮放恢復（一次 NVAPI set，可能重新編號——但此時 `s.saved` 已經被消費並清空）。

`Session.saved` 是一份 `domain.Layout`，它以 `DeviceName` 記住了**全部四台**顯示器的模式與座標。它是這個程式裡壽命最長的一組裝置名稱，也因此是最脆弱的一組。R1 加 R2 合起來保證的就是：**`saved` 從建立到清空的整段期間，沒有任何一次 NVAPI set。**

一句話記住：**進場先 GPU 後模式，離場先模式後 GPU。**

這條規則還有一個直接後果：**顯示恢復失敗時，縮放恢復不執行。** 顯示恢復失敗會保留擁有權讓使用者重試（既有設計），而重試需要穩定的裝置名稱；此時插一次可能重新編號的 set，等於親手把重試打壞。

### R3 — 名稱的存活範圍被壓到單一函式

`scaling.Controller` 的簽章不收也不回 `domain.Target`。`Session` 這一側，`domain.Target` 與 `domain.Layout` 都是 `Enable` / `restoreSaved` 的**函式區域變數**，在縮放呼叫之後建立、在縮放呼叫之前消費完畢。縮放的呼叫由一個既不收也不回 target／layout 的 helper 發出。

同一條規則的另一半：**NVAPI set 只發生在工作流程的邊界，永遠不在 `ApplyLayout` 進行中。** `applyLayout` 是一串帶著 rollback 狀態的多次呼叫，而那份 rollback 狀態正是以裝置名稱為鍵的；在中間插一次 set 會讓整組 undo 資訊在半途失效。`opMu` 保證了不同工作流程之間的序列化，但「set 只在邊界」這一句必須被明說，因為它不是鎖能表達的東西。

### R4 — 為什麼這是滴水不漏，而不是盡力而為

上面三條都是排序，而排序靠人維持就會被改壞。讓它變成強制的是測試替身：

> `Session` 測試裡的假 `scaling.Controller` 與假 `display.Controller` **共用一個記錄器**。假的 scaling controller 在每一次成功的 `Apply` 之後，**把假顯示控制器裡每一台顯示器的 `DeviceName` 全部改名**（`\\.\DISPLAY1` → `\\.\DISPLAY7` …），同時保持 `HardwareID`、`MonitorIdentity` 與 displayId 不變。假的顯示控制器則對任何指名到已不存在的裝置的呼叫**直接失敗**。

也就是說，測試環境裡的重新編號是**每一次 set 都發生**，比實測到的最壞情況還要嚴苛（實測是三次 set 中有一次）。任何一條把名稱帶過 set 的程式路徑，都會在測試裡確定性地爆掉，而不是在使用者的桌面上機率性地爆掉。

這是這個問題上能拿到的最強保證。「沒有失效名稱」無法用閱讀證明，但可以做到兩件事：把名稱能存在的地方縮到一個函式，然後讓測試環境對任何漏網之魚都毫不留情。

第四層是既有程式碼已經有的防線，這裡只是點名它們仍然有效：`ResolveTarget` 在每個工作流程裡都是重新解析的；`CDS_TEST` 用的是剛解析出來的名稱；`verifyApplied` 會重讀桌面並與計畫比對，一台讀不回來的顯示器會被當成錯誤而不是成功。

### R5 — 管理中變更縮放：R2 接 R1，同一次操作

工具正在管理已套用的模式時（`Session.managed` 為真），使用者仍然可以按縮放按鈕——套用與還原都一樣。它不被拒絕，理由寫在「管理中的縮放循環」：使用者會注意到黑邊的時機，正好就是遊戲模式生效中的那一刻。

按下去執行的是一次**完整循環**，而不是一次縮放寫入。整個循環在**同一次 `opMu` 之下**，與 `Enable`、`Disable` 同級：

1. 取得 `opMu`，立刻 `stopWatcher()`。
2. **R2 的全部**：`PlanRestore(s.saved, ...)` → `CDS_TEST` → `ApplyLayout` → `verifyApplied`；成功之後 `managed = false`、`saved = domain.Layout{}`。
3. **NVAPI 階段**：一次縮放寫入（`VALIDATE_ONLY` 先於正式套用），然後讀回。
4. **R1 的全部**：全新的身分解析 → 全新的 `CurrentLayout` → 全新的 `PlanModeChange` → `CDS_TEST` → `ApplyLayout` → `verifyApplied`；成功之後以**第 4 步那份全新的 `CurrentLayout`** 建立新的 `saved`、`managed = true`，並 `startWatcher()`。
5. 釋放 `opMu`，**然後才** join 第 1 步停下的 watcher。

**這條規則沒有放寬 R1 或 R2，它是把兩者接起來。** `saved` 在 NVAPI 寫入**之前**就被消費並清空，在 NVAPI 寫入**之後**才從一次全新的讀取重建——所以 R2 那句「`saved` 從建立到清空的整段期間，沒有任何一次 NVAPI set」依然逐字成立，只是這一次它成立了兩段。沒有任何裝置名稱跨過那次 set：第 2 步用的名稱在第 3 步之前就消費完畢，第 4 步用的名稱在第 3 步之後才產生。

代價已知並接受：**螢幕會比原本多換兩次模式。** 這是為了讓順序規則保持完整而付的價，不是疏忽。

**第 4 步是重做，不是重用。** 它不得沿用第 2 步的 target、layout 或 plan，也不得沿用進入循環之前的任何一份：身分要重新解析、`CurrentLayout` 要重新讀、`PlanModeChange` 要重新規劃。這正是 R1 第 3 步那一句——顯示層對整張桌面的認知，完全建立在最後一次 set **之後**。

順帶一提，這也是 **R1 在第一版裡第一次變得非空轉**。第一版的縮放按鈕與遊戲模式切換不綁在一起，所以在這條規則出現之前，產品程式碼裡沒有任何一條路徑會在 `ResolveTarget` 之前發出 NVAPI set——R1 只被 R4 的共用記錄器強制著，沒有人真的走過它。完整循環讓 R1 成為一條會執行的生產路徑，R4 的假實作也因此從「預防性」變成「守著一條活路徑」：這是產品裡唯一一處失效名稱真的有機會走到 `ChangeDisplaySettingsExW` 的地方。

**watcher 在循環中不得動作，而機制是既有的那兩個，不是新發明的第三個。**

- `stopWatcher()` 在取消之前就先 `s.generation++`。一個已經在 `opMu` 門口等著的輪詢，拿到鎖時 `generation != s.generation` 立刻成立，`observeGame` 直接回傳 false，`tracker.Observe` 根本不會被呼叫。**一個在循環中途到期的延遲恢復因此不會插進來，因為它連被評估的機會都沒有。**
- 第 2 步之後 `s.managed` 會**合法地**變成 false。`observeGame` 的前置檢查本來就含 `!s.managed`，所以就算生成號那一關被繞過，這個 false 也只會讓輪詢結束，不會被讀成「遊戲結束了，該恢復」。
- 循環自己的三步之間不放 `opMu`，所以輪詢連門都進不來。

第 1 步停下的 watcher 必須在**釋放 `opMu` 之後**才 join，理由與 `Disable` 完全相同：那個 goroutine 可能正卡在 `opMu` 上。

循環成功結束時，watcher 由第 4 步的 `startWatcher()` 重新開始，**帶著一個全新的 `gameTracker` 物件，但它的「看過遊戲」那一個布林值是接續下來的。** 這兩件事必須分開講，因為混在一起就會得到錯的結論：

- **不接續的是倒數。** `missing`、`missingAt`、`fired` 一律從零開始。走了一半的倒數如果被帶過循環，會在使用者剛剛明確要求回到遊戲模式的幾秒之後發出一次他沒有要求的恢復；而 `fired` 如果被帶過來，那個 tracker 永遠不會再武裝第二次——這正是 `ensureWatcher` 的既有註解說「替換的 watcher 從全新 tracker 開始」的理由，那個理由一字不變。**保留的是欄位，不是物件。**
- **接續的是 `seen`。** `Session` 多一個 `gameSeen bool`，生命週期與 `managed` 完全相同：`observeGame` 拿到 `result.SeenGame` 為真就把它設為真，`finishRestore` 釋放擁有權時清為假，第一次 `Enable` 之前是假。`startWatcher` 以它為種子建立 tracker，而且只設 `seen` 這一個欄位。

結果是**出貨承諾原封不動地跨過循環**：遊戲如果正好在循環進行中結束，循環之後第一次輪詢發現它不在，就從那一刻起算一次**完整長度**的延遲，然後照常恢復。沒有半截的計時器活下來，也沒有任何一次自動恢復被吞掉。

這也正是對使用者正確的行為：**恢復是為了「遊戲結束了」而存在的，而按下縮放按鈕並沒有表達「遊戲關掉之後我還要留在遊戲模式」。** 把待處理的恢復吞掉，等於用一個與它完全無關的動作取消它。

同一顆種子順帶讓 `ensureWatcher()` 那條保活路徑真的做到它註解裡承諾的事：一次自動恢復失敗之後，替換的 watcher 現在帶著 `seen = true` 上場，會在再一次完整延遲之後自己重試，而不是要等到遊戲又被看到一次才可能重新武裝。`fired` 沒有被帶過來，所以不會立刻重觸發，兩次嘗試之間一定隔著一次完整延遲。

---

## 讀回與 UI 呈現

主視窗多一行與一顆按鈕，全部由讀回來的值產生：

```text
GPU 縮放：全螢幕（由 GPU 執行）                ← Raw=2
GPU 縮放：長寬比（由顯示器執行）                ← Raw=6
GPU 縮放：無法讀取——找不到 NVIDIA 驅動          ← 不可用
[ 設為全螢幕縮放（GPU） ]   /   [ 還原 GPU 縮放設定 ]
```

按鈕文字依擁有權切換：本次執行還沒改過就是「設為全螢幕縮放（GPU）」，改過了就是「還原 GPU 縮放設定」，括號裡印出會被還原成的那個值。

三種結果，三種措辭：

| 結果 | 呈現 | 算不算錯誤 |
|---|---|---|
| 生效值 == 要求值 | 「GPU 縮放：全螢幕（由 GPU 執行）」 | 成功 |
| 生效值 != 要求值 | 「已要求『全螢幕（由 GPU 執行）』，驅動實際套用的是『全螢幕（由顯示器執行）』。」兩個值都印出來。 | **不是錯誤**：沒有東西壞掉、沒有東西需要回復。但按鈕**不會**被畫成「已啟用」的樣子，畫面顯示的是生效值。 |
| 呼叫失敗 | 帶出 NVAPI 狀態名稱與訊息 | 錯誤 |

**一條誠實邊界必須寫在畫面上，不是只寫在 README 裡。** 即使生效值是 `2`，工具也無法證明遊戲裡真的滿版：「覆寫遊戲和程式所設定的縮放模式」沒有 NVAPI 介面，工具讀不到它，遊戲仍然可能覆寫掉這個設定。因此當使用者選的模式與面板原生比例不同時，那一行提醒（profile 設計已承諾的那一行）保留，並改寫成：

> 「若遊戲內仍有黑邊，請到 NVIDIA 控制台勾選『覆寫遊戲和程式所設定的縮放模式』——這一項工具無法代為設定。」

同樣地，**工具永遠不宣稱「黑邊已經消失」**。它宣稱的只有「驅動現在回報的縮放設定是什麼」。

---

## 與啟用／恢復的互動

### 第一版：獨立的手動按鈕，不綁 4:3 切換

理由有三，而且第二條是決定性的：

1. **每一次 set 都可能重新編號顯示器。** 綁進 4:3 工作流程，等於把整個程式裡最危險的那條路徑的重新編號曝險加倍。
2. **恢復不保證忠實。** 實測寫 `6` 讀回 `1`。綁進切換就代表每一次開關循環都要寫回原值，而工具無法保證那個原值真的被寫回去——使用者的縮放設定會在一次 4:3 開關之後從 `6` 悄悄變成 `1`。**去還原一個你無法保證還原得回去的值，比從頭到尾不碰它更糟。**
3. 縮放是一個跨遊戲的全域偏好，多數使用者設一次就不想再被動。既有 spec 的基調本來就是「使用者明確要求才動」。

（這一項是「未解問題 1」，另一個選項與它的代價寫在那裡。）

**不綁在切換上，不等於在切換生效時停用。** `Session.managed` 為真時這顆按鈕仍然可按，只是按下去跑的是一次完整循環（R5）——理由、狀態呈現與三個失敗點見下方「管理中的縮放循環」。

### 擁有權與保存的狀態

模型與既有的 `managed` / `saved` 一模一樣，而且**完全獨立**：

```go
// Session 新增，與 managed / saved 平行且互不影響
scalingOwned bool   // 只有在本次執行成功改過縮放之後才為真
scalingSaved Value  // 改之前讀到的「生效值」，不是猜的、不是預設值
scalingID    uint32 // 當時對應到的 displayId

// 與縮放無關，但完整循環需要它：與 managed 同壽命的「本次擁有期間看過遊戲」旗標，
// startWatcher 以它為種子建立 gameTracker（只設 seen，不帶倒數，也不帶 fired）。
gameSeen bool
```

- `scalingSaved` 存的是**改之前最後一次讀回來的生效值**。工具不從任何地方推導「原本應該是什麼」。
- 沒有任何路徑會在 `scalingOwned` 為假時去寫縮放。啟動時、找不到螢幕時、讀取失敗時，一律不寫。
- **縮放的失敗永遠不影響模式切換的可用性，而且永遠不會自己動到 `managed` 或 `saved`。** 兩套擁有權完全分離。管理中的循環看起來像例外，其實不是：在那條路徑上動到 `managed` 與 `saved` 的，是循環第 2 步那次**成功的顯示恢復**，不是之後那次失敗的縮放呼叫。

### 管理中的縮放循環

**裁決：`Session.managed` 為真時按下縮放按鈕不被拒絕，它執行一次完整循環（R5）。**

被否決的是那條看起來最乾淨的規則——「`managed` 為真時，縮放的套用與還原一律拒絕並說明原因」。它是讓 R2 成立的最省事寫法，而且與 profile 設計停用「設定…」的規則長得一模一樣。**否決的理由只有一條，但它是決定性的：使用者會注意到黑邊的時機，正好就是遊戲模式生效中的那一刻。** 一顆在唯一需要它的時候停用的按鈕，等於沒有這顆按鈕。

套用與還原兩顆按鈕行為完全相同，都跑循環。

#### 循環進行中的狀態與狀態列

循環是**一次操作**，不是三次。它有自己的 `State`：

```go
StateScalingCycle State = "scaling-cycle"
```

狀態本身從頭到尾不變，狀態列依階段換字：

```text
正在變更 GPU 縮放：恢復原始排列…
正在變更 GPU 縮放：寫入縮放設定…
正在變更 GPU 縮放：重新套用 1920 × 1440 @ 180 Hz…
```

`Snapshot.Managed` 在**整個循環期間維持 true**，即使中間那一段內部旗標 `s.managed` 是 false。兩者不同步是刻意的，而且不是粉飾：`Snapshot.Managed` 唯一的消費者是 UI 的控制項啟用邏輯與「恢復原始解析度」按鈕，在循環中途把它翻成 false，只會在按下去毫無意義的那一瞬間把切換畫成「啟用」。循環的每一個出口都把 `Snapshot.Managed` 修正成當下真正的事實。

`StateScalingCycle` 期間，**所有會改變狀態的控制項都停用**——模式切換、恢復、設定…、以及縮放按鈕自己。`opMu` 已經保證了正確性（任何點擊都只會排隊等鎖），停用是為了不讓使用者對著一個正在動的桌面下第二道指令。

#### 三個失敗點

| 失敗點 | 縮放寫入發生了嗎 | 桌面停在哪 | `managed` / `saved` | `scalingOwned` |
|---|---|---|---|---|
| 1　顯示恢復（第 2 步）失敗 | **完全沒有呼叫 NVAPI** | 仍在遊戲模式 | **保留**，可重試 | 不變 |
| 2　縮放寫入（第 3 步）失敗 | 嘗試過，未成功 | 已恢復的原始排列 | 已清空 | 不變 |
| 3　重新套用（第 4 步）失敗 | **成功** | 已恢復的原始排列 | 維持清空 | **照那次成功的寫入更新**（套用→取得；還原→釋放） |

**失敗點 1：顯示恢復在第 2 步失敗。** 既有規則是「顯示恢復失敗保留擁有權讓使用者重試」，這裡一個字都不改。連帶的結論是**縮放請求不得繼續**：`saved` 還活著，此時發一次可能重新編號的 NVAPI set，正是 R2 明文禁止的事，也會把重試所需要的那組裝置名稱打壞。循環在任何 NVAPI 呼叫之前中止，第 1 步停下的 watcher 由 `ensureWatcher()` 放回去，與 `Disable` 的失敗路徑完全相同。訊息必須同時說兩件事——恢復失敗的原因，以及「GPU 縮放沒有被變更」——否則使用者無法判斷該重試哪一件。

**失敗點 2：縮放寫入在第 3 步失敗。** 顯示已經恢復、擁有權已經清空。**裁決：不重新套用遊戲模式，把使用者留在已恢復的排列上。** 三個理由：

1. 桌面此刻停在一個**已知良好**的狀態——正好是使用者啟用遊戲模式之前的那一組排列。沒有東西壞掉，沒有東西需要搶救。
2. 一次失敗的 set **仍然可能已經部分生效，也仍然可能已經重新編號**（「錯誤與恢復」表裡「正式套用非 OK」那一列就是為此要求重讀的）。在一次結果未知的 set 之後，立刻對四台顯示器規劃並套用一次**使用者沒有要求**的排列變更，是在錯誤狀態上再疊一次寫入。R1 確實規定此時要重新解析，照做也確實安全；但「安全」不等於「應該做」。
3. 既有 spec 的基調是**使用者明確要求才動**。這一按要的是縮放改變，工具做不到；把他送回遊戲模式是工具自己決定的第二件事，而且會讓錯誤訊息變得無法解讀——失敗的到底是顯示還是縮放？

結束狀態：`managed = false`、`saved` 為空、`scalingOwned` 不變（原本是 false 就仍是 false），`State` 為 `StateError`。訊息帶出 NVAPI 狀態名稱與 `NvAPI_GetErrorMessage` 文字，並且照既有規則**仍然重新讀一次縮放並顯示生效值**。模式切換此時是可用的，且顯示為「啟用」——使用者要回去只需要一次點擊，而那一次點擊走的是平常的 `Enable`，它本來就從頭解析一切。

**失敗點 3：重新套用遊戲模式在第 4 步失敗。** 縮放改成功了，使用者要的模式卻沒了。**裁決：顯示這一側不取得任何擁有權；縮放這一側照那次成功的寫入更新擁有權。**

- `managed` 維持 false、`saved` 維持空。桌面停在原始排列——那是工具**沒有**改過的狀態，沒有什麼需要被恢復；硬記一份「半擁有權」只會讓之後的 `Disable` 與 `Shutdown` 去恢復一個它不曾造成的排列。
- 縮放的寫入**真的成功了**，所以它照平常的規則記帳，與循環是否走完無關：按的是「設為全螢幕縮放（GPU）」就 `scalingOwned = true`、`scalingSaved` 記下寫入**之前**讀到的生效值、`scalingID` 記下當時的 displayId，按鈕切換成「還原 GPU 縮放設定」；按的是「還原 GPU 縮放設定」就 `scalingOwned = false`，按鈕切回「設為全螢幕縮放（GPU）」。第 4 步失敗不會把這一筆帳倒回去——那次寫入是既成事實，忘記它會讓退出時還原不了。

訊息要把兩半都說清楚，因為它們的結果相反：

> 「GPU 縮放已變更為『全螢幕（由 GPU 執行）』，但重新套用 1920 × 1440 @ 180 Hz 失敗：<原因>。桌面維持在原始排列。」

結束狀態剛好等於「平常的未管理狀態，加上縮放擁有權」——沒有任何特殊的復原路徑需要存在，重試就是再按一次模式切換。

兩個裁決共用同一句原則：**循環絕不持有它沒有成功套用的模式，也絕不持有它沒有成功保存的排列。擁有權只登記真正發生過的事。**

#### 與 `Shutdown` 的關係：兩條路，不會被混淆

`Shutdown` 的順序不變，一個字都不改：停 watcher → 顯示恢復 → 縮放還原（若擁有）→ `closed = true` → 關通知機制。它與循環的差別是結構性的，不是程度上的：

| | 完整循環 | `Shutdown` |
|---|---|---|
| 形狀 | 離場（R2）**接**進場（R1） | 只有離場（R2） |
| 縮放那一步做什麼 | 套用**使用者要求的新值** | 還原成 `scalingSaved` |
| 結束時工具擁有什麼 | 又擁有一次模式（成功時） | 什麼都不擁有 |
| 縮放失敗的後果 | 循環在該點停止並回報，桌面留在已恢復的排列 | 只發一次系統匣氣泡，**絕不阻擋退出** |
| 顯示恢復失敗的後果 | 保留擁有權、`ensureWatcher()`、中止循環 | 保留擁有權、`ensureWatcher()`、**工具不退出** |

兩者共用同一把 `opMu`，所以永遠不會交錯：先拿到鎖的那一個跑完。使用者在關閉工具的同時按下縮放按鈕，若 `Shutdown` 先拿到鎖，那一次按鍵落在已關閉的 session 上，得到 `ErrClosed`——既有行為，不需要新規則。

#### 為什麼「設定…」仍然停用，而縮放不用

profile 設計那條規則**不受本裁決影響**：`managed` 為真時「設定…」仍然停用，原因照舊寫在旁邊。兩者的差別不是嚴格程度，而是被威脅的東西不同：

- **改設定會讓 `saved` 的「意義」作廢。** `saved` 是**對著某一台目標顯示器、某一個目標模式**錄下來的一份排列。換掉目標螢幕或目標模式之後，`restoreSaved` 會解析到另一台顯示器，再去一份圍繞著別台顯示器錄下來的排列裡找它。沒有任何順序能修好這件事——壞掉的是語義，不是名稱。
- **改縮放只威脅 `saved` 的「裝置名稱」。** 而名稱是可以重新求得的：先消費 `saved`、再寫、再從一次全新的讀取重建它。完整循環之所以可行，正是因為受損的是一個可重新求得的東西。

還有一個實務差別：設定改錯的失敗是**安靜的**（幾分鐘後某一次恢復悄悄套到錯的螢幕上），縮放循環的失敗是**當場而且吵的**（三個失敗點各自有訊息與明確的結束狀態）。

### 什麼時候還原

| 事件 | 還原縮放？ |
|---|---|
| 使用者按「還原 GPU 縮放設定」 | 是 |
| 使用者在工具管理模式期間按縮放按鈕（套用或還原皆同） | 是——但整件事跑的是完整循環：先恢復顯示、再寫縮放、再重新套用模式（R5、「管理中的縮放循環」） |
| 工具退出（`Shutdown`） | 是——但在顯示恢復成功**之後**（R2），且**永遠不阻擋退出** |
| 使用者關閉 4:3 切換 | 否（第一版兩者不綁） |
| 被觀察的程序結束 | 否（同上） |
| 工具或系統非正常終止 | 無法清理。因為不帶持久化旗標，值是 runtime-only，重開機或驅動重載即回到使用者在控制台裡的設定。 |

**縮放還原永遠不阻擋 `Shutdown`。** 顯示模式還原失敗會讓工具留著不退出（既有設計，因為螢幕真的停在錯的模式上）；縮放還原失敗不會，因為那個值是 runtime-only、使用者一次重開機或一次控制台點擊就能處理，而一個退不掉的工具比一個縮放設定沒還原的工具糟得多。失敗會在系統匣氣泡裡說一次，然後退出。

---

## 錯誤與恢復

既有 spec 與 profile 設計的錯誤清單全部保留。新增：

| 情況 | 行為 |
|---|---|
| `nvapi64.dll` 不存在／進入點解析不到／`NvAPI_Initialize` 失敗 | `ErrNvapiUnavailable`。按鈕停用並寫出原因與偵測到的顯示卡名稱。「重新整理」會重新探測一次（使用者可能剛裝完驅動）。 |
| 目標螢幕對應不到 NVIDIA displayId | `ErrNotNvidiaDisplay`。按鈕停用，訊息指名該廠商的控制台。 |
| displayId 對到 0 個 target | `ErrScalingTargetNotFound`。不寫。 |
| displayId 對到 2 個以上 target | `ErrScalingTargetAmbiguous`。列出候選，不寫——這在單一 displayId 下不該發生，發生就代表假設壞了。 |
| 變更前後的差異不只那一行 scaling | `ErrScalingPayloadDiverged`。**不寫**，並且把功能停用到本次執行結束：結構佈局與驅動不再相符，繼續嘗試是拿整張桌面冒險。 |
| `NVAPI_INCOMPATIBLE_STRUCT_VERSION (-9)` | 自己的訊息：「這個驅動版本的結構約定與工具不同」。停用到本次執行結束，**不用猜出來的版本重試**。 |
| `VALIDATE_ONLY` 非 OK | 中止，不做正式套用。帶出狀態名稱與 `NvAPI_GetErrorMessage` 文字。桌面未被改動。 |
| 正式套用非 OK | 回報錯誤，**然後仍然重新讀一次並顯示生效值**——失敗的 set 可能已經部分生效，使用者需要知道現在到底是什麼。 |
| 生效值 != 要求值 | 不是錯誤。見「讀回與 UI 呈現」。 |
| 縮放還原時目標螢幕已不在 | 中止還原，保留擁有權讓使用者重試，訊息說明螢幕不見了。退出時則只提示一次就放行。 |
| 管理中的循環：顯示恢復（第 2 步）失敗 | 中止循環，**完全不呼叫 NVAPI**。保留 `managed` 與 `saved` 讓使用者重試，`ensureWatcher()` 把 watcher 放回去，訊息同時說明恢復失敗的原因與「GPU 縮放未變更」。 |
| 管理中的循環：縮放寫入（第 3 步）失敗 | 中止循環，**不重新套用遊戲模式**。桌面留在已恢復的原始排列，`managed = false`、`saved` 已清空、`scalingOwned` 不變。照既有規則仍然重讀一次縮放並顯示生效值。 |
| 管理中的循環：重新套用遊戲模式（第 4 步）失敗 | 縮放的擁有權照那次**成功的**寫入更新（套用→取得，還原→釋放），顯示的擁有權**不取得**（`managed` 維持 false、`saved` 維持空）。訊息同時印出縮放的新生效值與重新套用失敗的原因；重試就是再按一次模式切換。 |
| 任何縮放錯誤 | **永遠不改變模式切換的可用性**，也**永遠不自己動到 `managed` 或 `saved`**。管理中的循環看起來像例外，其實不是：動到那兩個欄位的是第 2 步那次**成功的顯示恢復**，不是之後那次失敗的縮放呼叫。 |

最後一列不只是一句話：`internal/ui` 現在的 `updateAvailability` 會把 `unavailableReason` **latch** 住，而那個字串會透過 `availableControls` 裡的單一個 `interactive` 旗標，一次停掉切換、恢復與啟用。縮放的錯誤如果流進那個 latch，會把 4:3 切換一起關掉。UI 因此需要一個**獨立的第二組可用性狀態**，只管縮放那顆按鈕。

---

## 尚未驗證的事

誠實列出，因為不列出來就會在使用者手上被發現。

- **把 `2` 寫在非原生解析度下的行為從來沒有測過。** 所有的寫入實驗都在面板原生的 `2560×1440` 下進行。而這個功能存在的理由**正是**非原生的情況（`1920×1440` 在 16:9 面板上）。實測行為一的正規化推論——驅動在原生解析度下把模式那一半收斂掉——如果成立，非原生下就不該發生；但這是推論，不是量測。**發布前的人工 smoke test 必須先切到 4:3、再寫 `2`、再讀回，並記錄結果。**
- **重新編號是非決定性的。** 三次 set 裡有一次改了編號。無法預測哪一次會改，所以只能當成每一次都可能。順序規則是照這個最壞情況設計的。
- **「覆寫遊戲和程式所設定的縮放模式」關閉時，VALORANT 會不會覆寫掉這個設定**，沒有測過。工具讀不到那個核取方塊，所以也無法偵測。
- **只在一張 RTX 3070 加一個驅動版本（32.0.16.1074）上測過。** 結構版本是編進驅動的 ABI，舊驅動可能回 `-9`；這條路徑有設計（停用並說明），但沒有在真的舊驅動上跑過。
- **兩張 GPU 的機器上，寫入第一張 GPU 的 path 會影響第二張 GPU 的顯示器編號**——這一點測到了。但**在只有一張 GPU 的機器上會不會完全不發生**，沒有測過，設計不依賴它。

---

## 測試策略

CI runner 上沒有 NVIDIA GPU，也沒有 `nvapi64.dll`。策略因此是「把所有有判斷的東西推到假實作上面」，並且明確承認哪一塊只有真硬體跑得到。

### 不需要 GPU（CI 會跑）

- **結構大小與版本常數。** `unsafe.Sizeof` 對 `pathInfo`（48）／`advTargetInfo`（128）／`targetInfo`（24）／`sourceMode`（32）／`timing`（96）／`timingExt`（64）的斷言是純 Go layout，任何 amd64 平台都成立，包含 CI。由它們算出的 `0x00020030` 與 `0x00010080` 一併斷言。**這是整份測試裡 CP 值最高的一項，而且完全不需要 GPU。** 以 `runtime.GOARCH == "amd64"` 為條件。
- **`NV_SCALING` 解碼。** 表格驅動：`2` → (FullScreen, GPU)、`6` → (AspectRatio, Display)、`1` → (FullScreen, Display)、`8` → (IntegerScaling, GPU)、`255` → Customized、`4` → 未定義值（枚舉裡沒有 4）要被當成「無法識別」而不是靜默當成某個已知值。
- **旗標守衛。** `checkFlags` 接受 `0x00` 與 `0x01`，拒絕 `0x02` 與其他任何位元；並且有一個測試掃過整個套件，斷言 `0x02` 只出現在常數宣告那一行。
- **恰好一個 target。** 假設定裡放 0 個、1 個、2 個相符的 displayId，分別要拿到 NotFound / OK / Ambiguous，**而且永遠不取第一個**。
- **差異檢查。** 假 `nvapi` 在 `readConfig` 的第二次呼叫回傳一份被動了另一個欄位的設定 → `ErrScalingPayloadDiverged`，且 `writeConfig` 呼叫次數為 0。
- **呼叫序列。** 假 `nvapi` 記錄每一次 `writeConfig` 的旗標字。斷言：恰好兩次、第一次是 `0x01`、第二次是 `0x00`；`VALIDATE_ONLY` 回非 OK 時只有一次呼叫且旗標是 `0x01`。
- **正規化。** 假 `nvapi` 在寫入 `6` 之後讀回 `1`（重現實測）→ 拿到 `Outcome{Matched: false}`，不是 error，而且 `State.Effective.Raw == 1`。
- **失敗的 set 仍然重讀。** 假 `nvapi` 讓正式套用回非 OK → 斷言 `readConfig` 仍被再呼叫一次。
- **不可用路徑。** 假 DLL 載入失敗 → `ErrNvapiUnavailable`，且 `initialize` / `readConfig` / `writeConfig` 呼叫次數皆為 0。**CI 上真正端到端跑到的就是這一條**，所以它必須有測試。
- **順序規則（R4）。** `internal/app` 的 session 測試用共用記錄器的假實作：假 scaling controller 每一次成功 `Apply` 之後就把假顯示控制器的所有 `DeviceName` 改名，假顯示控制器對任何指到不存在裝置的呼叫直接失敗。涵蓋：`Enable` 全程、`Disable` 全程、`Shutdown` 全程、以及顯示恢復失敗時縮放恢復不得執行。
- **完整循環（R5）。** 與 R4 用同一組共用記錄器的假實作——循環正是**最需要**它的那條路徑，因為它是產品程式碼裡唯一一條會在 `ResolveTarget` 之前發出 NVAPI set 的路徑，一個跨過去的舊名稱在這裡會直接改到別台螢幕。涵蓋：循環的順利路徑（恢復 → 寫縮放 → 重新套用，且第 4 步的身分解析、`CurrentLayout` 與 plan 全部是新的）；新的 `saved` 由第 4 步那份全新 layout 建立，不是由被消費掉的那一份；三個失敗點各自的完整結束狀態（見「管理中的縮放循環」的表）；**循環進行中 watcher 不可能動作**——用可控時鐘讓一次延遲恢復在循環中途到期，斷言 `tracker.Observe` 不被呼叫、桌面只被改動兩次；以及**遊戲在循環中途結束時，自動恢復仍然會發生**——斷言它在循環後第一次「看到程序不在」的輪詢起算一次完整延遲之後觸發，不是從循環之前起算，也不是永遠不觸發，而替換 tracker 的 `missing` / `missingAt` / `fired` 全為零值。
- **擁有權隔離。** 縮放的每一種失敗都不得改變切換的可用性，也都不得自己動到 `managed` 或 `saved`；反之，顯示的每一種失敗都不得改變 `scalingOwned` 或 `scalingSaved`。管理中的循環要單獨講一次：那條路徑上 `managed` 與 `saved` 確實會變，但動到它們的是第 2 步那次**成功的顯示恢復**，斷言要寫成「失敗的縮放呼叫前後，這兩個欄位沒有再變過」。
- **UI 可用性。** `availableControls` 的縮放那一組與既有的 `interactive` 互不影響：縮放不可用時 4:3 切換仍然可用，反之亦然。

### 需要真硬體（opt-in，預設跳過，CI 永不設定）

- **唯讀整合測試**，以**新的**環境變數 `RUN_NVAPI_INTEGRATION=1` 開啟。**不得重用 `RUN_DISPLAY_INTEGRATION`**——那個變數承諾的是「只有 `CDS_TEST` 與唯讀列舉」，混用會讓那句承諾變得沒有意義。內容：解析 displayId、三段式讀取、以及故意用錯版本的**負向控制**（必須拿到 `-9`）。這一段覆蓋的正是介面縫下面那塊三段式指標綁定的程式碼，也就是假實作測不到的那塊。
- **寫入驗證不是自動測試，是發布前的人工步驟。** 理由就是實測行為二：**每一次 set 都可能重新編號顯示器**，包括寫回完全相同的值。一個會重排使用者桌面的「測試」不該被任何人不小心跑到。人工項目：
  1. 在原生解析度下讀出目前值並記錄。
  2. 切到 4:3，**然後**寫 `2`，讀回並記錄——這是「尚未驗證的事」第一條要補的量測。
  3. 確認四台螢幕的解析度、刷新率與色彩深度未變，桌面沒有空隙或重疊。
  4. 還原縮放，讀回並記錄是否忠實。
  5. 每一步都比對 `\\.\DISPLAYn` 有沒有改變，並記錄。

---

## 與既有程式碼的張力

不列出來就會在實作時被發現。

- **`domain.Target` 是以名稱為鍵的，而且它會被發佈到 UI。** `Snapshot.Target` 帶著 `DeviceName`，`ui.targetText` 把它印在畫面上。一次縮放寫入之後，畫面上那個名稱可能已經失效。工作流程尾端必須補一次讀取，讓 snapshot 的名稱跟上——這不影響正確性（UI 不拿它去呼叫任何東西），但會影響使用者對畫面的信任。
- **`Session.saved` 記的是全部四台的名稱，不只目標。** `restoreSaved` 現在只檢查**目標**還在不在（`s.saved.Find(target.DeviceName)`）；一台被改名的**鄰居**會通過這個檢查，然後在 `stageChanges` 的 `EnumDisplaySettingsW` 上失敗。失敗是安全的（什麼都沒寫），但訊息會讓人看不懂發生什麼事。R1／R2 讓這件事不會發生，但那句錯誤訊息值得順手改好。
- **`ui.updateAvailability` 的 latch 是單一個 `interactive` 旗標。** 縮放需要第二組獨立的可用性狀態，否則一個縮放錯誤會連帶關掉 4:3 切換。這是 UI 這一側最實際的一塊改動。
- **主視窗的空間已經有人在搶。** profile 設計已經把它從 `420×240` 放到約 `460×280`、加了第四顆按鈕，並且已經寫明「若排不下就先把『隱藏至系統匣』移進系統匣選單」。本設計再加一行文字與一顆按鈕。兩份設計要一起看，不能各自加。
- **`Snapshot` 被兩份設計同時動到。** profile 設計要把 `FourByThree` 一般化（例如改成 `AtGameMode`）並把寫死的 4:3 字串改成由設定生成；本設計要加縮放的欄位。同一個結構、同一個發布，必須協調。
- **`Session.Shutdown()` 的順序要重排。** 目前是「停 watcher → restore → `closed = true` → 關通知機制」。縮放還原要插在 restore **成功之後**、`closed = true` **之前**，而且不得因失敗而回到 `ensureWatcher` 那條保活路徑。
- **`Snapshot.Managed` 與 `Session.managed` 在循環中途會不一致。** 內部旗標在第 2 步之後合法地變成 false，而發布出去的快照維持 true（理由見「管理中的縮放循環」）。這是整個結構裡唯一一處刻意讓兩者不同步的地方，所以它必須在 `updateSnapshot` 的呼叫端寫明——它讀起來像 bug，下一個人會「順手修好」它。`ui.updateAvailability` / `availableControls` 也要認得 `StateScalingCycle`，在循環期間把每一個會改變狀態的控制項停用。
- **指標存在結構欄位裡，是這個 repo 沒有過的危險類別。** `internal/display` 只傳 `*devMode`，指標從不落進結構體。NVAPI 的 path 結構裡放的是 `uintptr`，GC 追不到，`go vet` 的 unsafeptr 也抓不到。`runtime.KeepAlive` 的紀律必須是明文的 review 檢查項，不是「大家都知道」。
- **`CGO_ENABLED=0` 不受影響。** 整條路徑走 `LoadLibrary` 加 `nvapi_QueryInterface` 加 `syscall.SyscallN`，不需要 cgo，`build.ps1` 與 workflow 不必改。
- **`domain.MonitorIdentity` 是 profile 設計引進的型別。** 本設計的 `Controller` 直接依賴它。兩份設計要一起落地，或者本設計先以現有的硬體 ID 前綴為身分、待 profile 設計落地後再換——後者會讓兩台同型號顯示器的歧義問題在縮放這一側也重演一次。建議一起做。

---

## 已定案

作者已裁決，取代下方對應的建議：

- **未解問題 1（觸發方式）：** 第一版採獨立手動按鈕，不綁 4:3 切換。連帶確定：縮放只在使用者明確按下還原、或工具退出時還原，不跟著 4:3 開關走、也不跟著遊戲結束走。日後若要改成綁定，前提是先補完「尚未驗證的事」第一條，並把還原改成有狀態判斷——屆時是一次刻意的設計變更，不是補丁。
- **管理中按下縮放按鈕（新增裁決，取代「未解問題 1」建議裡「兩者完全不互動」的推論）：** 曾經的候選規則是「`Session.managed` 為真時，縮放的套用與還原一律拒絕並說明原因」——它是讓 R2 成立的最省事寫法，也與 profile 設計停用「設定…」的規則長得一樣。**否決。** 使用者會注意到黑邊的時機，正好就是遊戲模式生效中的那一刻；一顆在唯一需要它的時候停用的按鈕，等於沒有這顆按鈕。
  **改為：`managed` 為真時按下縮放按鈕（套用或還原皆同）執行一次完整循環——恢復顯示排列 → 寫入縮放 → 重新套用遊戲模式。** 這不是放寬順序規則，而是把 R2 接上 R1：`saved` 在 NVAPI 寫入之前就被消費並清空，在寫入之後才從一次全新的讀取重建，所以沒有任何裝置名稱跨過那次 set。規則寫在「順序規則」的 R5，循環中的狀態與狀態列、三個失敗點、以及與 `Shutdown`／「設定…」的關係寫在「與啟用／恢復的互動」的「管理中的縮放循環」。
  已知並接受的代價：**螢幕會比原本多換兩次模式。** 連帶確定的三項：顯示恢復失敗 → 循環中止且**完全不碰 NVAPI**，擁有權保留可重試；縮放寫入失敗 → **不**重新套用遊戲模式，使用者留在已恢復的原始排列上；重新套用失敗 → 縮放那一筆帳照**成功的**寫入記下（套用→取得，還原→釋放）、**不**取得顯示擁有權。
  **「設定…」在 `managed` 為真時仍然停用**，這一項不受影響：改設定讓 `saved` 的語義作廢（無法用順序修復），改縮放只威脅 `saved` 的裝置名稱（可以重新求得）。
- **未解問題 2、3、4、5：** 採用原建議。其中第 5 項的效果是：縮放還原失敗只發出一次系統匣通知，**絕不阻擋退出**；顯示模式還原失敗仍然保留工具不退出，兩者行為刻意不同。

## 未解問題

下列項目已由「已定案」裁決，保留原文以記錄當初的理由與被否決的替代方案。


1. **縮放要綁在 4:3 切換上自動執行，還是維持獨立的手動按鈕？還原時又該怎麼辦？**
   *建議：* 第一版獨立、手動；只在使用者明確按下還原與工具退出時還原，不跟著 4:3 開關走、也不跟著遊戲結束走。決定性的理由是**還原不保證忠實**——實測寫 `6` 讀回 `1`——所以綁在切換上會讓使用者的縮放設定在一次開關循環後從 `6` 變成 `1`。次要理由是每一次 set 都可能重新編號顯示器，綁上去等於把最危險路徑的曝險加倍。
   *另一個選項與它的代價：* 綁上去，使用者只按一顆按鈕就得到想要的結果（這確實是他真正想要的）。要走這條路，先決條件是把「尚未驗證的事」第一條補完（非原生解析度下寫 `2` 的往返是否忠實），而且還原要改成「只在生效值仍然等於本工具寫下的值時才還原」——使用者中途自己去控制台改過，工具就放手不動。這個條件本身可實作，但它讓還原邏輯從三行變成一個有狀態的判斷，所以必須現在決定，不能之後補。

2. **`scaling.Controller` 由 `Session` 持有，還是由 UI 直接驅動一個獨立物件？**
   *建議：* `Session` 持有。這不是品味問題：`Session.opMu` 是整個程式裡唯一序列化顯示工作流程的地方，R1、R2、R3 全部依賴「縮放寫入與顯示寫入在同一把鎖底下、而且順序由同一段程式碼決定」。改成獨立物件就要引進第二把鎖，而「哪一把先拿」會立刻變成一個沒有結構性答案的問題，順序規則也就退回成一句叮嚀。代價是 `Session` 又長大了一點，而 profile 設計已經提出要讓 UI 持有可替換的 session 提供者（未解問題 9）——兩者相容，縮放控制器跟著 session 一起重建即可。

3. **要不要把 `internal/display` 從「以 `\\.\DISPLAYn` 定址」改成「以穩定鍵定址」（adapter LUID 加 targetId，或 NVAPI 的 displayId）？**
   *建議：* 這一版不做。那是這個危害的**根治**方案——名稱失效在結構上就不可能，順序也就不再重要——但它要動 `domain.Target`、`domain.Layout`、`LayoutPlan`、整個 `win32_windows.go`、以及所有既有測試，而且 `ChangeDisplaySettingsExW` 最終仍然只收名稱，所以它只是把重新解析集中到一個地方，不是消滅它。R1 到 R4 用小得多的成本換到足夠的保證。但這一項要留著：**如果之後再出現第二個會讓名稱失效的來源**（熱插拔、驅動更新、另一個廠商 API），就該做這個改動，而不是再疊一條順序規則。

4. **讀到的縮放值要不要反過來影響 4:3 的啟用——當使用者選的模式是非原生比例、而生效的縮放不是 GPU 全螢幕時？**
   *建議：* 提醒，永遠不拒絕。profile 設計已經承諾在挑到非原生比例時顯示一行提醒，但那是一行**靜態字串**；有了讀取路徑之後，它可以變成一行**量測出來的**提醒：「這個模式是 4:3，而目前的 GPU 縮放是『長寬比（由顯示器執行）』，畫面會有黑邊。」這把兩個功能在**讀的方向**耦合起來，而寫的方向仍然完全分離——這正是第一版想要的形狀。拒絕則絕對不行：使用者可能就是想要黑邊，或是已經在別處處理過了。

5. **縮放還原失敗，算不算一次失敗的還原？**
   *建議：* 不算，而且**永遠不阻擋退出**。顯示模式還原失敗會讓工具留著不退出，那是對的——螢幕真的停在錯的模式上。縮放不一樣：值是 runtime-only，重開機或驅動重載就回到使用者控制台裡的設定，而且它不影響任何東西的可用性。一個退不掉的工具比一個縮放沒還原的工具糟得多。做法是：系統匣氣泡說一次，然後退出。這一項會改變 `Shutdown` 的控制流，所以必須現在決定。
