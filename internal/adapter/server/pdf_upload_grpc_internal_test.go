package server

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
)

// TestPDFUploadGRPCDefaultReceiveTimeout pairs the real service's zero-value
// configuration with the receive bound used by UploadPdf. The transport tests
// exercise expiry with a short explicit timeout.
func TestPDFUploadGRPCDefaultReceiveTimeout(t *testing.T) {
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: memstore.New(),
		PlacementProvider: titlePlacementProvider{}, PlacementScope: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got := pdfUploadReceiveTimeout(svc.cfg.PDFUploadReceiveTimeout); got != 30*time.Minute {
		t.Fatalf("zero-config gRPC PDF upload receive timeout = %s, want finite 30m bound", got)
	}
}
