import win32api
import win32con

def change_resolution(width, height):
    device = win32api.EnumDisplayDevices(None, 0)
    settings = win32api.EnumDisplaySettings(device.DeviceName, win32con.ENUM_CURRENT_SETTINGS)
    settings.PelsWidth = width
    settings.PelsHeight = height
    settings.Fields = win32con.DM_PELSWIDTH | win32con.DM_PELSHEIGHT
    win32api.ChangeDisplaySettings(settings, win32con.CDS_UPDATEREGISTRY)

def get_current_resolution():
    device = win32api.EnumDisplayDevices(None, 0)
    settings = win32api.EnumDisplaySettings(device.DeviceName, win32con.ENUM_CURRENT_SETTINGS)
    return settings.PelsWidth, settings.PelsHeight

current_width, current_height = get_current_resolution()

if current_width == 1920 and current_height == 1080:
    change_resolution(1440, 1080)
    print("分辨率已從 1920x1080 更改為 1440x1080")
elif current_width == 1440 and current_height == 1080:
    change_resolution(1920, 1080)
    print("分辨率已從 1440x1080 更改為 1920x1080")
else:
    change_resolution(1920, 1080)
    print("分辨率已設置為 1920x1080")