package domain

import "time"

type Mode struct {
	Width        uint32
	Height       uint32
	RefreshHz    uint32
	BitsPerPixel uint32
}

type Target struct {
	DeviceName string
	HardwareID string
}

type Profile struct {
	Name               string
	MonitorHardwareID  string
	GameMode           Mode
	FallbackNativeMode Mode
	ProcessName        string
	RestoreDelay       time.Duration
}

func DefaultProfile() Profile {
	return Profile{
		Name:               "Mi Monitor 4:3",
		MonitorHardwareID:  `MONITOR\XMI27B2`,
		GameMode:           Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		FallbackNativeMode: Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		ProcessName:        "VALORANT-Win64-Shipping.exe",
		RestoreDelay:       3 * time.Second,
	}
}
