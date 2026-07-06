package proc

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Steam", "steam"},
		{"steam.exe", "steam"},
		{"/usr/bin/steam", "steam"},
		{"  Game.EXE  ", "game"},
	}

	for _, tc := range cases {
		if got := normalize(tc.input); got != tc.want {
			t.Fatalf("normalize(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
