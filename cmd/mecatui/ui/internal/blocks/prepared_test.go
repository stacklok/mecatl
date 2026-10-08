package blocks

import "testing"

func TestPreparedTextJoinsLines(t *testing.T) {
	prepared := Prepared{Lines: []string{"first", "second", "third"}}
	if got, want := prepared.Text(), "first\nsecond\nthird"; got != want {
		t.Errorf("Prepared.Text() = %q, want %q", got, want)
	}
}
