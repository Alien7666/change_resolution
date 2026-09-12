package domain

import (
	"testing"
	"time"
)

func TestDefaultProfile(t *testing.T) {
	p := DefaultProfile()
	if p.MonitorHardwareID != `MONITOR\XMI27B2` {
		t.Fatalf("MonitorHardwareID = %q", p.MonitorHardwareID)
	}
	if p.GameMode != (Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}) {
		t.Fatalf("GameMode = %#v", p.GameMode)
	}
	if p.ProcessName != "VALORANT-Win64-Shipping.exe" || p.RestoreDelay != 3*time.Second {
		t.Fatalf("process/delay = %q/%s", p.ProcessName, p.RestoreDelay)
	}
}
