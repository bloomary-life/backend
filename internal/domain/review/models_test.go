package review

import "testing"

func TestWordDiffMarksMemberEdits(t *testing.T) {
	// Verifies: BE-014
	got := WordDiff("Hiring is hard for every team", "Hiring is slow for most teams")
	want := "Hiring is [-hard-]{+slow+} for [-every team-]{+most teams+}"
	if got != want {
		t.Fatalf("WordDiff = %q, want %q", got, want)
	}
}
