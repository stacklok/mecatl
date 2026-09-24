package pdfartifact

import (
	"bufio"
	"bytes"
	"context"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type s3HTTPFixture struct {
	mu             sync.Mutex
	objects        map[string][]byte
	parts          map[string]map[int][]byte
	multipartCount int
	completeCount  int
	blockKey       string
	partStarted    chan struct{}
	partRelease    chan struct{}
}

func (f *s3HTTPFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/private-pdf" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		f.mu.Lock()
		keys := make([]string, 0, len(f.objects))
		for key := range f.objects {
			if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
				keys = append(keys, key)
			}
		}
		f.mu.Unlock()
		slices.Sort(keys)
		response := struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			Name        string   `xml:"Name"`
			IsTruncated bool     `xml:"IsTruncated"`
			Contents    []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
		}{Name: "private-pdf"}
		for _, key := range keys {
			response.Contents = append(response.Contents, struct {
				Key string `xml:"Key"`
			}{Key: key})
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(response)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/private-pdf/") || r.Header.Get("Authorization") == "" {
		http.Error(w, "unexpected unsigned or non-path-style request", http.StatusBadRequest)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/private-pdf/")
	query := r.URL.Query()
	if _, initiate := query["uploads"]; initiate && r.Method == http.MethodPost {
		f.mu.Lock()
		f.multipartCount++
		f.parts[key] = make(map[int][]byte)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, "<InitiateMultipartUploadResult><Bucket>private-pdf</Bucket><Key>%s</Key><UploadId>fixture-upload</UploadId></InitiateMultipartUploadResult>", key)
		return
	}
	if query.Get("uploadId") != "" {
		switch r.Method {
		case http.MethodPut:
			f.mu.Lock()
			block := f.blockKey == key
			started := f.partStarted
			release := f.partRelease
			f.mu.Unlock()
			if block {
				select {
				case <-started:
				default:
					close(started)
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
			partNumber, err := strconv.Atoi(query.Get("partNumber"))
			if err != nil {
				http.Error(w, "invalid part", http.StatusBadRequest)
				return
			}
			body, err := readS3RequestBody(r)
			if err != nil {
				return
			}
			f.mu.Lock()
			f.parts[key][partNumber] = body
			f.mu.Unlock()
			w.Header().Set("ETag", fmt.Sprintf("\"part-%d\"", partNumber))
			return
		case http.MethodPost:
			f.mu.Lock()
			parts := f.parts[key]
			numbers := make([]int, 0, len(parts))
			for number := range parts {
				numbers = append(numbers, number)
			}
			slices.Sort(numbers)
			var full []byte
			for _, number := range numbers {
				full = append(full, parts[number]...)
			}
			f.objects[key] = full
			delete(f.parts, key)
			f.completeCount++
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, "<CompleteMultipartUploadResult><Bucket>private-pdf</Bucket><Key>completed</Key><ETag>\"complete\"</ETag></CompleteMultipartUploadResult>")
			return
		case http.MethodDelete:
			f.mu.Lock()
			delete(f.parts, key)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("Content-Type") != "application/pdf" {
			http.Error(w, "wrong content type", http.StatusBadRequest)
			return
		}
		body, err := readS3RequestBody(r)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.objects[key] = body
		f.mu.Unlock()
		w.Header().Set("ETag", "\"single\"")
	case http.MethodGet:
		f.mu.Lock()
		body, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(body)
	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unsupported request", http.StatusMethodNotAllowed)
	}
}

// The real SDK sends streaming checksums with S3's aws-chunked content coding.
// Decode the HTTP fixture request before comparing it with stored PDF bytes.
func readS3RequestBody(r *http.Request) ([]byte, error) {
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		return io.ReadAll(r.Body)
	}
	reader := bufio.NewReader(r.Body)
	var decoded bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeHex, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		size, err := strconv.ParseInt(sizeHex, 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			for {
				trailer, err := reader.ReadString('\n')
				if err != nil {
					return nil, err
				}
				if trailer == "\r\n" {
					return decoded.Bytes(), nil
				}
			}
		}
		if _, err := io.CopyN(&decoded, reader, size); err != nil {
			return nil, err
		}
		if _, err := reader.Discard(2); err != nil {
			return nil, err
		}
	}
}

func newS3HTTPFixture(t *testing.T) (*S3Objects, *s3HTTPFixture) {
	t.Helper()
	fixture := &s3HTTPFixture{objects: make(map[string][]byte), parts: make(map[string]map[int][]byte)}
	server := httptest.NewTLSServer(fixture)
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "s3-fixture-ca.pem")
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, cert, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CA_BUNDLE", caPath)
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	objects, err := NewS3(t.Context(), S3Config{Bucket: "private-pdf", Region: "eu-west-1", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return objects, fixture
}

func TestPDFArtifactStorage_S3AdapterRequests(t *testing.T) {
	objects, fixture := newS3HTTPFixture(t)
	const key = "pdf/v1/session/report.pdf"
	want := []byte("%PDF-1.7\nfixture\n%%EOF")
	if err := objects.Put(t.Context(), key, bytes.NewReader(want)); err != nil {
		t.Fatalf("SDK PutObject: %v", err)
	}
	reader, err := objects.Open(t.Context(), key)
	if err != nil {
		t.Fatalf("SDK GetObject: %v", err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("streamed object = %q, read %v, close %v", got, readErr, closeErr)
	}
	keys, err := objects.ListPrefix(t.Context(), "pdf/v1/session/")
	if err != nil || !slices.Equal(keys, []string{key}) {
		t.Fatalf("SDK ListObjectsV2 = %v, %v", keys, err)
	}
	if err := objects.Delete(t.Context(), key); err != nil {
		t.Fatalf("SDK DeleteObject: %v", err)
	}
	keys, err = objects.ListPrefix(t.Context(), "pdf/v1/session/")
	if err != nil || len(keys) != 0 {
		t.Fatalf("objects after delete = %v, %v", keys, err)
	}
	fixture.mu.Lock()
	multipartCount := fixture.multipartCount
	fixture.mu.Unlock()
	if multipartCount != 0 {
		t.Fatalf("small PutObject started %d multipart uploads", multipartCount)
	}
}

func TestPDFArtifactStorage_S3MultipartAndCancellation(t *testing.T) {
	objects, fixture := newS3HTTPFixture(t)
	large := bytes.Repeat([]byte("p"), 5<<20+1)
	const key = "pdf/v1/session/large.pdf"
	if err := objects.Put(t.Context(), key, bytes.NewReader(large)); err != nil {
		t.Fatalf("SDK multipart upload: %v", err)
	}
	reader, err := objects.Open(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil || !bytes.Equal(got, large) {
		t.Fatalf("multipart bytes: size=%d read=%v", len(got), readErr)
	}
	fixture.mu.Lock()
	started, completed := fixture.multipartCount, fixture.completeCount
	fixture.blockKey = "pdf/v1/session/cancelled.pdf"
	fixture.partStarted = make(chan struct{})
	fixture.partRelease = make(chan struct{})
	partStarted := fixture.partStarted
	partRelease := fixture.partRelease
	fixture.mu.Unlock()
	defer close(partRelease)
	if started != 1 || completed != 1 {
		t.Fatalf("multipart SDK requests: initiated=%d completed=%d", started, completed)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- objects.Put(ctx, "pdf/v1/session/cancelled.pdf", bytes.NewReader(large)) }()
	select {
	case <-partStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("multipart part request never began")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStorage) {
			t.Fatalf("cancelled SDK upload = %v, want storage error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled multipart upload did not return")
	}
	fixture.mu.Lock()
	_, published := fixture.objects[fixture.blockKey]
	completed = fixture.completeCount
	fixture.mu.Unlock()
	if published || completed != 1 {
		t.Fatalf("cancelled multipart published=%v completed=%d", published, completed)
	}
}
