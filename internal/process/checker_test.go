package process

import "testing"

func TestMatchesExecutableRequiresExactCaseInsensitiveName(t *testing.T) {
	const target = "VALORANT-Win64-Shipping.exe"
	for _, name := range []string{"VALORANT-Win64-Shipping.exe", "valorant-win64-shipping.EXE"} {
		if !matchesExecutable(name, target) {
			t.Errorf("matchesExecutable(%q, %q) = false, want true", name, target)
		}
	}
	for _, name := range []string{"RiotClientServices.exe", "VALORANT.exe", "VALORANT-Win64-Shipping", "xVALORANT-Win64-Shipping.exe"} {
		if matchesExecutable(name, target) {
			t.Errorf("matchesExecutable(%q, %q) = true, want false", name, target)
		}
	}
}
