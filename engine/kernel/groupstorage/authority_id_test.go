package groupstorage_test

import (
	"strings"
	"testing"

	resourceid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/groupstorage"
)

func TestAcquisitionID(t *testing.T) {
	value := groupstorage.NewAcquisitionID()
	parsed, err := groupstorage.ParseAcquisitionID(value.String())
	if err != nil || parsed != value || value.ResourceID().Kind() != "ACQ" {
		t.Fatalf("round trip: %v, %v", parsed, err)
	}
	if !map[groupstorage.AcquisitionID]bool{value: true}[parsed] {
		t.Fatal("equal acquisition IDs must be interchangeable map keys")
	}
	var zero groupstorage.AcquisitionID
	if zero.String() != "" || zero.ResourceID() != (resourceid.ID{}) {
		t.Fatal("zero acquisition ID must remain unset")
	}
	for _, text := range []string{"", groupstorage.NewGeneration().String(), "GEN" + strings.Repeat("1", 22)} {
		got, err := groupstorage.ParseAcquisitionID(text)
		if err == nil || got != zero {
			t.Errorf("ParseAcquisitionID(%q) = %v, %v; want zero and error", text, got, err)
		}
	}
}

func TestGeneration(t *testing.T) {
	value := groupstorage.NewGeneration()
	parsed, err := groupstorage.ParseGeneration(value.String())
	if err != nil || parsed != value || value.ResourceID().Kind() != "GEN" {
		t.Fatalf("round trip: %v, %v", parsed, err)
	}
	if !map[groupstorage.Generation]bool{value: true}[parsed] {
		t.Fatal("equal generations must be interchangeable map keys")
	}
	var zero groupstorage.Generation
	if zero.String() != "" || zero.ResourceID() != (resourceid.ID{}) {
		t.Fatal("zero generation must remain unset")
	}
	for _, text := range []string{"", groupstorage.NewAcquisitionID().String(), "ACQ" + strings.Repeat("1", 22)} {
		got, err := groupstorage.ParseGeneration(text)
		if err == nil || got != zero {
			t.Errorf("ParseGeneration(%q) = %v, %v; want zero and error", text, got, err)
		}
	}
}
