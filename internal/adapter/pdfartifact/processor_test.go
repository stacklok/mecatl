package pdfartifact

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

func newResultProcessorStore(t *testing.T, objects ObjectStore) *Store {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	if err := metadata.Save(t.Context(), session.New("pdf-result-owner", session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))); err != nil {
		t.Fatal(err)
	}
	return New(metadata, objects)
}

func pdfResult(blob []byte, uri string) session.ToolResult {
	return session.NewToolResultWithParts("call-1", "untrusted producer summary", []session.Content{
		session.NewTextBlock("plain before PDF"),
		{BlockKind: session.BlockEmbeddedResource, MIMEType: "application/pdf", URL: uri, Data: blob},
		session.NewTextBlock("plain after PDF"),
	})
}

func TestSDKPDFArtifacts_Scenario2_ExternalizeEffectiveResult(t *testing.T) {
	objects := &memoryObjects{data: make(map[string][]byte)}
	store := newResultProcessorStore(t, objects)
	pdf := []byte("%PDF-1.7\nprivate contents\n%%EOF")
	got, err := (ResultProcessor{Artifacts: store}).ProcessToolResult(t.Context(), "pdf-result-owner", pdfResult(pdf, "https://host.invalid/../../hostile.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if got.CallID != "call-1" || got.IsError || len(got.Parts) != 3 || got.Parts[1].BlockKind != session.BlockPDFArtifact {
		t.Fatalf("processed result = %+v", got)
	}
	block := got.Parts[1]
	if block.ArtifactID == "" || block.Name != "artifact.pdf" || block.Size != int64(len(pdf)) || block.SHA256 == "" || len(block.Data) != 0 {
		t.Fatalf("PDF reference = %+v", block)
	}
	if !strings.Contains(got.Content, "plain before PDF") || !strings.Contains(got.Content, "PDF artifact: artifact.pdf") || !strings.Contains(got.Content, "plain after PDF") || strings.Contains(got.Content, string(pdf)) || len(got.Content) > session.MaxToolResultTextBytes {
		t.Fatalf("model summary = %q", got.Content)
	}
	_, reader, err := store.Open(t.Context(), "pdf-result-owner", block.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stored, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(stored, pdf) {
		t.Fatalf("stored bytes = %q, %v", stored, err)
	}
	engineObjects := &memoryObjects{data: make(map[string][]byte)}
	engineStore := newResultProcessorStore(t, engineObjects)
	event, snapshot, audit, model := runArtifactResult(t, ResultProcessor{Artifacts: engineStore}, pdfResult(pdf, "ignored://uri"), nil)
	assertArtifactViews(t, pdf, event, snapshot, audit, model)
	t.Run("20 MiB PDF with text", func(t *testing.T) {
		objects := &memoryObjects{data: make(map[string][]byte)}
		store := newResultProcessorStore(t, objects)
		atLimit := make([]byte, MaxPDFBytes)
		copy(atLimit, []byte("%PDF-1.7\n"))
		copy(atLimit[len(atLimit)-len("\n%%EOF"):], []byte("\n%%EOF"))
		got, err := (ResultProcessor{Artifacts: store}).ProcessToolResult(t.Context(), "pdf-result-owner", pdfResult(atLimit, ""))
		if err != nil || got.CallID != "call-1" || got.IsError || len(got.Parts) != 3 {
			t.Fatalf("processor rejected mixed 20 MiB PDF: callID=%q isError=%v parts=%d err=%v", got.CallID, got.IsError, len(got.Parts), err)
		}
		block := got.Parts[1]
		if block.BlockKind != session.BlockPDFArtifact || block.Size != MaxPDFBytes || len(block.Data) != 0 || len(objects.data) != 1 ||
			!strings.Contains(got.Content, "plain before PDF") || !strings.Contains(got.Content, "PDF artifact: artifact.pdf") ||
			!strings.Contains(got.Content, "plain after PDF") || strings.Contains(got.Content, "%PDF-") {
			t.Fatalf("processor boundary lost artifact or summary: block=%+v objects=%d summary=%q", block, len(objects.data), got.Content)
		}
	})
	t.Run("two PDF blocks", func(t *testing.T) {
		objects := &memoryObjects{data: make(map[string][]byte)}
		store := newResultProcessorStore(t, objects)
		input := session.NewToolResultWithParts("call-2", "binary resources", []session.Content{
			{BlockKind: session.BlockEmbeddedResource, MIMEType: "application/pdf", Data: pdf},
			session.NewTextBlock("between"),
			{BlockKind: session.BlockEmbeddedResource, MIMEType: "application/pdf", Data: pdf},
		})
		got, err := (ResultProcessor{Artifacts: store}).ProcessToolResult(t.Context(), "pdf-result-owner", input)
		if err != nil || len(got.Parts) != 3 || got.Parts[0].BlockKind != session.BlockPDFArtifact || got.Parts[2].BlockKind != session.BlockPDFArtifact || got.Parts[0].ArtifactID == got.Parts[2].ArtifactID || len(objects.data) != 2 {
			t.Fatalf("two PDF blocks = %+v, %v; stored=%d", got, err, len(objects.data))
		}
	})
}

type failingObjects struct{ memoryObjects }

func (*failingObjects) Put(context.Context, string, io.Reader) error {
	return errors.New("storage failure with private PDF bytes")
}

func TestSDKPDFArtifacts_Scenario2_FailClosed(t *testing.T) {
	pdf := []byte("%PDF-1.7\nprivate contents\n%%EOF")
	for _, tc := range []struct {
		name    string
		objects ObjectStore
		result  session.ToolResult
	}{
		{"bad signature", &memoryObjects{data: make(map[string][]byte)}, pdfResult([]byte("not a pdf %%EOF"), "")},
		{"oversized", &memoryObjects{data: make(map[string][]byte)}, pdfResult(append([]byte("%PDF-"), bytes.Repeat([]byte("x"), MaxPDFBytes)...), "")},
		{"duplicate raw summary", &memoryObjects{data: make(map[string][]byte)}, func() session.ToolResult { r := pdfResult(pdf, ""); r.Content = string(pdf); return r }()},
		{"duplicate base64 text", &memoryObjects{data: make(map[string][]byte)}, func() session.ToolResult {
			r := pdfResult(pdf, "")
			r.Parts[0].Text = base64.StdEncoding.EncodeToString(pdf)
			return r
		}()},
		{"object failure", &failingObjects{memoryObjects{data: make(map[string][]byte)}}, pdfResult(pdf, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newResultProcessorStore(t, tc.objects)
			got, err := (ResultProcessor{Artifacts: store}).ProcessToolResult(t.Context(), "pdf-result-owner", tc.result)
			if err == nil || got.CallID != "" || len(got.Parts) != 0 {
				t.Fatalf("failed result = %+v, %v", got, err)
			}
		})
	}
	objects := &memoryObjects{data: make(map[string][]byte)}
	store := newResultProcessorStore(t, objects)
	removedByHook := session.NewToolResult("call-1", "hook removed PDF")
	got, err := (ResultProcessor{Artifacts: store}).ProcessToolResult(t.Context(), "pdf-result-owner", removedByHook)
	if err != nil || got.Content != removedByHook.Content || len(objects.data) != 0 {
		t.Fatalf("post-hook removal = %+v, %v, stored=%d", got, err, len(objects.data))
	}
	engineObjects := &memoryObjects{data: make(map[string][]byte)}
	engineStore := newResultProcessorStore(t, engineObjects)
	event, snapshot, audit, model := runArtifactResult(t, ResultProcessor{Artifacts: engineStore}, pdfResult(pdf, "ignored://uri"), removePDFHook{})
	for index, view := range []session.ToolResult{event, snapshot, audit, model} {
		if view.CallID != "call-1" || view.Content != "hook removed PDF" || len(view.Parts) != 0 {
			t.Fatalf("post-hook view %d = %+v", index, view)
		}
	}
	if len(engineObjects.data) != 0 {
		t.Fatalf("removed PDF left %d objects", len(engineObjects.data))
	}
	for _, tc := range []struct {
		name    string
		objects ObjectStore
		result  session.ToolResult
		blob    []byte
	}{
		{"bad signature", &memoryObjects{data: make(map[string][]byte)}, pdfResult([]byte("%PDF-bad"), "ignored://uri"), []byte("%PDF-bad")},
		{"oversized", &memoryObjects{data: make(map[string][]byte)}, pdfResult(append([]byte("%PDF-"), bytes.Repeat([]byte("x"), MaxPDFBytes)...), ""), []byte("%PDF-")},
		{"duplicate raw", &memoryObjects{data: make(map[string][]byte)}, func() session.ToolResult { r := pdfResult(pdf, ""); r.Content = string(pdf); return r }(), pdf},
		{"duplicate base64", &memoryObjects{data: make(map[string][]byte)}, func() session.ToolResult {
			r := pdfResult(pdf, "")
			r.Parts[0].Text = base64.StdEncoding.EncodeToString(pdf)
			return r
		}(), pdf},
		{"object failure", &failingObjects{memoryObjects{data: make(map[string][]byte)}}, pdfResult(pdf, ""), pdf},
	} {
		t.Run("engine "+tc.name, func(t *testing.T) {
			store := newResultProcessorStore(t, tc.objects)
			event, snapshot, audit, model := runArtifactResult(t, ResultProcessor{Artifacts: store}, tc.result, nil)
			assertPDFErrorViews(t, tc.blob, event, snapshot, audit, model)
		})
	}
}

func TestSDKPDFArtifacts_Scenario2_SafeName(t *testing.T) {
	for _, uri := range []string{"", "../../secret.pdf", "https://evil.invalid/passport.pdf", "\x00secret.pdf"} {
		t.Run(uri, func(t *testing.T) {
			objects := &memoryObjects{data: make(map[string][]byte)}
			store := newResultProcessorStore(t, objects)
			got, err := (ResultProcessor{Artifacts: store}).ProcessToolResult(t.Context(), "pdf-result-owner", pdfResult([]byte("%PDF-1.7\n%%EOF"), uri))
			if err != nil || got.Parts[1].Name != "artifact.pdf" {
				t.Fatalf("URI %q produced %+v, %v", uri, got, err)
			}
		})
	}
}
