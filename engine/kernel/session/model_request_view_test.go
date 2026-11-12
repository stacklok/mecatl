package session

import (
	"bytes"
	"testing"
)

func TestConstructionRecipeViewFieldsAndOwnership(t *testing.T) {
	s := interactionFixture(t, "pending")
	record := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1)
	request, ok := record.ModelRequest()
	if !ok {
		t.Fatal("missing request")
	}
	recipe := request.Recipe()
	if recipe.Algorithm() != "unknown/construction" || recipe.Version() != 9 || !bytes.Equal(recipe.Parameters().Copy(), []byte{0xff, 0}) || !bytes.Equal(recipe.Check().Copy(), []byte{1}) {
		t.Fatal("recipe fields")
	}
	parameters := recipe.Parameters().Copy()
	check := recipe.Check().Copy()
	parameters[0], check[0] = 0, 0
	if !bytes.Equal(recipe.Parameters().Copy(), []byte{0xff, 0}) || !bytes.Equal(recipe.Check().Copy(), []byte{1}) {
		t.Fatal("recipe byte exports alias")
	}

	var zero ModelRequestView
	if zero.Source().Sequence() != 0 || zero.Source().CID() != (SourceHead{}).CID {
		t.Fatal("zero source head")
	}
	zeroRecipe := zero.Recipe()
	if zeroRecipe.Algorithm() != "" || zeroRecipe.Version() != 0 || zeroRecipe.Parameters().Copy() != nil || zeroRecipe.Check().Copy() != nil {
		t.Fatal("zero recipe")
	}
}
