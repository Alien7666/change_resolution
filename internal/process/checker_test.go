package process

import "testing"

func TestUniqueSortedNamesDeduplicatesCaseInsensitivelyAndKeepsFirstSpelling(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "drops empty entries",
			input: []string{"", "Alpha.exe", ""},
			want:  []string{"Alpha.exe"},
		},
		{
			name:  "keeps first spelling",
			input: []string{"beta.exe", "BETA.EXE", "Alpha.exe", "ALPHA.EXE"},
			want:  []string{"Alpha.exe", "beta.exe"},
		},
		{
			name:  "sorts case insensitively",
			input: []string{"zeta.exe", "Gamma.exe", "alpha.exe", "DELTA.EXE"},
			want:  []string{"alpha.exe", "DELTA.EXE", "Gamma.exe", "zeta.exe"},
		},
		{
			name:  "returns nil for no names",
			input: []string{"", ""},
			want:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := uniqueSortedNames(test.input)
			if len(got) != len(test.want) {
				t.Fatalf("uniqueSortedNames(%q) = %q, want %q", test.input, got, test.want)
			}
			for i := range test.want {
				if got[i] != test.want[i] {
					t.Errorf("uniqueSortedNames(%q)[%d] = %q, want %q", test.input, i, got[i], test.want[i])
				}
			}
		})
	}
}

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
