package process

import "testing"

func TestContainsExecutableUsesExactCaseInsensitiveName(t *testing.T) {
	names := []string{"RiotClientServices.exe", "valorant-win64-shipping.EXE"}
	if !containsExecutable(names, "VALORANT-Win64-Shipping.exe") {
		t.Fatal("expected match")
	}
	if containsExecutable(names, "VALORANT.exe") {
		t.Fatal("unexpected partial match")
	}
}
