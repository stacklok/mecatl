package yamldiag

import (
	"errors"
	"strings"
	"testing"
)

func TestGoccyYAMLMigration_RootDocumentParsesOneMappingWithSafeLocations(t *testing.T) {
	t.Parallel()

	doc, err := ParseSettingsDocument([]byte("selected: value\n"))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Mapping() == nil {
		t.Fatal("Mapping() = nil, want parsed mapping")
	}

	_, err = ParseSettingsDocument([]byte("secret: ["))
	if err == nil {
		t.Fatal("malformed YAML unexpectedly parsed")
	}
	var documentErr *DocumentError
	if !errors.As(err, &documentErr) {
		t.Fatalf("error = %T, want *DocumentError", err)
	}
	if !documentErr.Location.HasLocation {
		t.Fatalf("location = %#v, want parser token location", documentErr.Location)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaks YAML content: %q", err)
	}
}

func TestRootDocumentRejectsEmptyDocumentWithoutPanicking(t *testing.T) {
	t.Parallel()

	_, err := ParseSettingsDocument(nil)
	if !errors.Is(err, ErrNonMappingRoot) {
		t.Fatalf("ParseSettingsDocument(nil) error = %v, want %v", err, ErrNonMappingRoot)
	}
}

func TestRootDocumentUsesNoLocationWhenNoTokenIsAvailable(t *testing.T) {
	t.Parallel()

	err := newDocumentError(ErrParseDocument, nil)
	if err.Location.HasLocation || err.Location.Line != 0 || err.Location.Column != 0 {
		t.Fatalf("location = %#v, want no invented location", err.Location)
	}
}

func TestRootDocumentRejectsAmbiguousOrUnsafeShapesBeforeDecode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		document string
		want     error
	}{
		{name: "multiple documents", document: "first: value\n---\nsecond: value\n", want: ErrMultipleDocuments},
		{name: "non mapping root", document: "- value\n", want: ErrNonMappingRoot},
		{name: "duplicate mapping key", document: "repeated: first\nrepeated: second\n", want: ErrParseDocument},
		{name: "anchor", document: "item: &saved value\n", want: ErrAnchor},
		{name: "alias", document: "item: *saved\n", want: ErrAlias},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseSettingsDocument([]byte(tc.document))
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseSettingsDocument() error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "repeated") || strings.Contains(err.Error(), "saved") {
				t.Fatalf("error leaks YAML content: %q", err)
			}
		})
	}
}

func TestRootDocumentDecodesOnlyCallerSelectedNode(t *testing.T) {
	t.Parallel()

	doc, err := ParseSettingsDocument([]byte("selected: expected\nignored:\n  nested: value\n"))
	if err != nil {
		t.Fatal(err)
	}
	selected := doc.Mapping().Values[0].Value

	var got string
	if err := doc.Decode(selected, &got); err != nil {
		t.Fatal(err)
	}
	if got != "expected" {
		t.Fatalf("selected node decode = %q, want expected", got)
	}
	if err := doc.Decode(nil, &got); !errors.Is(err, ErrDecodeNode) {
		t.Fatalf("Decode(nil) error = %v, want %v", err, ErrDecodeNode)
	}
}
