// Package tlsreload owns hot reload and expiry observation for a server TLS certificate.
package tlsreload

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/filewatch"
)

const (
	reloadDebounce         = 100 * time.Millisecond
	reloadMaxDebounce      = time.Second
	expiryPollInterval     = time.Hour
	expiryWarningThreshold = 30 * 24 * time.Hour
	maxTLSFileSize         = 1 << 20
)

type publishedCertificate struct {
	certificate *tls.Certificate
	generation  uint64
}

type ticker interface {
	Chan() <-chan time.Time
	Stop()
}

type realTicker struct{ *time.Ticker }

func (t realTicker) Chan() <-chan time.Time { return t.C }

type dependencies struct {
	now       func() time.Time
	newTicker func(time.Duration) ticker
	watcher   func([]string, time.Duration, time.Duration, func(), func(error)) (*filewatch.Watcher, error)
}

func defaultDependencies() dependencies {
	return dependencies{
		now:       time.Now,
		newTicker: func(d time.Duration) ticker { return realTicker{time.NewTicker(d)} },
		watcher:   filewatch.New,
	}
}

// Reloader atomically publishes only complete, currently valid certificate chains.
type Reloader struct {
	certificate       atomic.Pointer[publishedCertificate]
	generation        atomic.Uint64
	diagnostics       port.Diagnostics
	certFile          string
	keyFile           string
	now               func() time.Time
	watcher           *filewatch.Watcher
	stop              chan struct{}
	done              chan struct{}
	closeOnce         sync.Once
	closeErr          error
	observeMu         sync.Mutex
	beforeObserveLock func()
	warned            uint64
}

// New synchronously loads and validates the initial certificate, starts the projected-file
// watcher, and starts expiry observation. The caller must Close the returned lifecycle.
func New(certFile, keyFile string, diagnostics port.Diagnostics) (*Reloader, error) {
	return newWithDependencies(certFile, keyFile, diagnostics, defaultDependencies())
}

func newWithDependencies(certFile, keyFile string, diagnostics port.Diagnostics, deps dependencies) (*Reloader, error) {
	if diagnostics == nil {
		diagnostics = port.NopDiagnostics{}
	}
	cert, err := loadServerCertificate(certFile, keyFile, deps.now())
	if err != nil {
		return nil, err
	}
	r := &Reloader{
		diagnostics: diagnostics,
		certFile:    certFile,
		keyFile:     keyFile,
		now:         deps.now,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	r.publish(cert)
	watcher, err := deps.watcher(
		[]string{certFile, keyFile},
		reloadDebounce,
		reloadMaxDebounce,
		r.reload,
		func(error) {
			r.log(port.LevelWarn, "TLS certificate watch error", "watch", "failure", "watch_error")
		},
	)
	if err != nil {
		return nil, errors.New("TLS keypair watcher setup failed")
	}
	r.watcher = watcher
	t := deps.newTicker(expiryPollInterval)
	go r.observeExpiry(t)
	r.observeCurrent()
	return r, nil
}

func (r *Reloader) publish(cert *tls.Certificate) {
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	generation := r.generation.Add(1)
	r.certificate.Store(&publishedCertificate{certificate: cert, generation: generation})
}

func (r *Reloader) reload() {
	cert, err := loadServerCertificate(r.certFile, r.keyFile, r.now())
	if err != nil {
		r.log(port.LevelWarn, "TLS certificate reload rejected; retaining last valid certificate", "reload", "failure", "invalid_candidate")
		return
	}
	r.publish(cert)
	r.log(port.LevelInfo, "TLS certificate reloaded", "reload", "success", "published")
	r.observeCurrent()
}

// GetCertificate supplies the currently published certificate to tls.Config.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.certificate.Load().certificate, nil
}

func (r *Reloader) observeExpiry(t ticker) {
	defer close(r.done)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.Chan():
			r.observeCurrent()
		}
	}
}

func (r *Reloader) observeCurrent() {
	if r.beforeObserveLock != nil {
		r.beforeObserveLock()
	}
	r.observeMu.Lock()
	defer r.observeMu.Unlock()
	// Publication can race an observation that began on the prior certificate.
	// Re-read while serialized so only the currently published generation may
	// advance warning state or emit a warning.
	published := r.certificate.Load()
	remaining := published.certificate.Leaf.NotAfter.Sub(r.now())
	if remaining > expiryWarningThreshold || r.warned == published.generation {
		return
	}
	r.warned = published.generation
	reason := "expiring"
	if remaining <= 0 {
		reason = "expired"
	}
	r.diagnostics.Log(context.Background(), port.LevelWarn, "TLS certificate expiry warning",
		"component", "tls_certificate", "operation", "observe_expiry", "outcome", "warning",
		"reason", reason, "remaining", remaining.Round(time.Second))
}

func (r *Reloader) log(level port.Level, message, operation, outcome, reason string) {
	r.diagnostics.Log(context.Background(), level, message,
		"component", "tls_certificate", "operation", operation, "outcome", outcome, "reason", reason)
}

// Close stops and joins the file watcher and expiry observer. It is idempotent.
func (r *Reloader) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closeErr = r.watcher.Close()
		if r.closeErr != nil {
			r.log(port.LevelWarn, "TLS certificate lifecycle shutdown failed", "shutdown", "failure", "shutdown_error")
		}
		close(r.stop)
		<-r.done
	})
	return r.closeErr
}

func readTLSFile(path string) ([]byte, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxTLSFileSize {
		return nil, nil, errors.New("invalid TLS file")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxTLSFileSize+1))
	if err != nil || len(body) > maxTLSFileSize {
		return nil, nil, errors.New("invalid TLS file")
	}
	return body, info, nil
}

// ReadCredentialFile reads a bounded regular credential file without blocking.
// It follows projected-volume symlinks and reads from the descriptor it verified.
func ReadCredentialFile(path string) ([]byte, error) {
	body, _, err := readTLSFile(path)
	if err != nil {
		return nil, errors.New("TLS credential load failed")
	}
	return body, nil
}

func sameTLSFile(path string, before os.FileInfo) bool {
	after, err := os.Stat(path)
	return err == nil && after.Mode().IsRegular() && os.SameFile(before, after)
}

func loadServerCertificate(certFile, keyFile string, now time.Time) (*tls.Certificate, error) {
	certPEM, certInfo, err := readTLSFile(certFile)
	if err != nil {
		return nil, errors.New("TLS keypair load failed")
	}
	keyPEM, keyInfo, err := readTLSFile(keyFile)
	if err != nil {
		return nil, errors.New("TLS keypair load failed")
	}
	if !sameTLSFile(certFile, certInfo) || !sameTLSFile(keyFile, keyInfo) {
		return nil, errors.New("TLS keypair load failed")
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, errors.New("TLS keypair load failed")
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("TLS certificate chain is empty")
	}
	parsed := make([]*x509.Certificate, 0, len(cert.Certificate))
	for _, der := range cert.Certificate {
		certificate, parseErr := x509.ParseCertificate(der)
		if parseErr != nil {
			return nil, errors.New("TLS certificate parse failed")
		}
		if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
			return nil, errors.New("TLS certificate is not currently valid")
		}
		parsed = append(parsed, certificate)
	}
	for i := 1; i < len(parsed); i++ {
		if err := parsed[i-1].CheckSignatureFrom(parsed[i]); err != nil {
			return nil, errors.New("TLS certificate chain validation failed")
		}
	}
	cert.Leaf = parsed[0]
	return &cert, nil
}
