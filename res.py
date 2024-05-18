import win32api
import win32con

def change_resolution(width, height):
    device = win32api.EnumDisplayDevices(None, 0)
    settings = win32api.EnumDisplaySettings(device.DeviceName, win32con.ENUM_CURRENT_SETTINGS)
    settings.PelsWidth = width
    settings.PelsHeight = height
    settings.Fields = win32con.DM_PELSWIDTH | win32con.DM_PELSHEIGHT
    win32api.ChangeDisplaySettings(settings, win32con.CDS_UPDATEREGISTRY)

print("請選擇螢幕 1 的分辨率:")
print("1. 1920x1080")
print("2. 1440x1080")

choice = input("輸入選項 (1 或 2): ")

if choice == "1":
    change_resolution(1920, 1080)
    print("螢幕 1 的分辨率已更改為 1920x1080")
elif choice == "2":
    change_resolution(1440, 1080)
    print("螢幕 1 的分辨率已更改為 1440x1080")
else:
    print("無效的選項")