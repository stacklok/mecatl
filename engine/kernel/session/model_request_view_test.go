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
	if recipe.Algorithm() != "unknown/construction" || recipe.Version() != 9 || !bytes.Equal(recipe.Parameters(), []byte{0xff, 0}) || !bytes.Equal(recipe.Check(), []byte{1}) {
		t.Fatal("recipe fields")
	}
	parameters := recipe.Parameters()
	check := recipe.Check()
	parameters[0], check[0] = 0, 0
	if !bytes.Equal(recipe.Parameters(), []byte{0xff, 0}) || !bytes.Equal(recipe.Check(), []byte{1}) {
		t.Fatal("recipe byte exports alias")
	}

	var zero ModelRequestView
	zeroRecipe := zero.Recipe()
	if zeroRecipe.Algorithm() != "" || zeroRecipe.Version() != 0 || zeroRecipe.Parameters() != nil || zeroRecipe.Check() != nil {
		t.Fatal("zero recipe")
	}
}
