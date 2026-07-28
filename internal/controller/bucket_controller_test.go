package controller

import "testing"

func TestPlacementConflict(t *testing.T) {
	cases := []struct {
		name          string
		specPlacement string
		livePlacement string
		want          bool
	}{
		{"unset spec never conflicts", "", "default-placement", false},
		{"unset spec unset live", "", "", false},
		{"matching ec", "ec", "ec", false},
		{"matching default", "default-placement", "default-placement", false},
		{"ec requested but default live", "ec", "default-placement", true},
		{"default requested but ec live", "default-placement", "ec", true},
		{"spec set live empty", "ec", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := placementConflict(tc.specPlacement, tc.livePlacement); got != tc.want {
				t.Errorf("placementConflict(%q,%q)=%v want %v", tc.specPlacement, tc.livePlacement, got, tc.want)
			}
		})
	}
}
