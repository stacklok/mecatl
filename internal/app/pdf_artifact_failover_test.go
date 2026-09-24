package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/pdfartifact"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

type pdfReplicaObjects struct{ data map[string][]byte }

func (o *pdfReplicaObjects) Put(_ context.Context, key string, source io.Reader) error {
	data, err := io.ReadAll(source)
	if err == nil {
		o.data[key] = data
	}
	return err
}

func (o *pdfReplicaObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := o.data[key]
	if !ok {
		return nil, errors.New("missing object")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (o *pdfReplicaObjects) Delete(_ context.Context, key string) error {
	delete(o.data, key)
	return nil
}

func (o *pdfReplicaObjects) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for key := range o.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func TestSDKPDFArtifacts_Scenario4_FailoverProviderHydration(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	newMetadata := func() *redisstore.Store {
		t.Helper()
		store, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	const id session.SessionID = "pdf-failover-model"
	firstMetadata := newMetadata()
	sess := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := firstMetadata.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	objects := &pdfReplicaObjects{data: make(map[string][]byte)}
	firstArtifacts := pdfartifact.New(firstMetadata, objects)
	want := []byte("%PDF-1.7\nprovider-failover-private\n%%EOF")
	meta, err := firstArtifacts.Stage(t.Context(), id, "report.pdf", bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	part, err := session.NewPDFContent(meta.ID, meta.Name, meta.Size, meta.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordUserPromptWithParts("read", []session.Content{part}, nil); err != nil {
		t.Fatal(err)
	}
	if err := (pdfPromptRedisStore{Store: firstMetadata, artifacts: firstArtifacts}).Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	secondMetadata := newMetadata()
	secondArtifacts := pdfartifact.New(secondMetadata, objects)
	loaded, err := secondMetadata.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var modelRequest port.LLMRequest
	provider := pdfReferenceProvider{inner: mockllm.NewWith([]mockllm.Option{
		mockllm.WithCapabilities(port.ProviderCapabilities{PDF: true}),
		mockllm.WithRequestObserver(func(req port.LLMRequest) { modelRequest = req }),
	}, mockllm.TextTurn("done")), artifacts: secondArtifacts}
	stream, err := provider.Stream(port.WithSessionID(t.Context(), id), port.LLMRequest{Messages: loaded.Conversation.Messages})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(modelRequest.Messages) != 1 || len(modelRequest.Messages[0].Parts) != 1 || !bytes.Equal(modelRequest.Messages[0].Parts[0].Data, want) {
		t.Fatal("replica B's PDF-capable provider did not receive the stored PDF bytes")
	}
	if len(loaded.Conversation.Messages[0].Parts[0].Data) != 0 || loaded.Conversation.Messages[0].Parts[0].ArtifactID != meta.ID {
		t.Fatal("provider hydration mutated replica B's loaded session")
	}
	if snapshot := mr.HGet("mecatl:session:"+string(id), "blob"); !strings.Contains(snapshot, meta.ID) || strings.Contains(snapshot, string(want)) {
		t.Fatal("replica B's model request changed the reference-only Redis snapshot")
	}
}
