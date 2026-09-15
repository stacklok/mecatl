// Command mecatl-execution-provider runs the authenticated Kubernetes execution provider.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func main() {
	if err := run(); err != nil {
		slog.Error("execution provider stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	var addr, namespace, profilesPath, certPath, keyPath, caPath, signingPath, keyID, issuer, audience, clients, attesters, admins string
	flag.StringVar(&addr, "listen", ":8443", "HTTPS listen address")
	flag.StringVar(&namespace, "namespace", "", "managed Kubernetes namespace")
	flag.StringVar(&profilesPath, "profiles", "/etc/mecatl-execution/profiles.yaml", "strict operator profile file")
	flag.StringVar(&certPath, "tls-cert", "/etc/mecatl-execution/tls/tls.crt", "server certificate")
	flag.StringVar(&keyPath, "tls-key", "/etc/mecatl-execution/tls/tls.key", "server private key")
	flag.StringVar(&caPath, "client-ca", "/etc/mecatl-execution/tls/ca.crt", "client CA bundle")
	flag.StringVar(&signingPath, "grant-signing-key", "/etc/mecatl-execution/grant/key.pem", "Ed25519 PKCS8 grant signing key")
	flag.StringVar(&keyID, "grant-key-id", "current", "grant verification key id")
	flag.StringVar(&issuer, "grant-issuer", "mecatl-execution-provider", "exact grant issuer")
	flag.StringVar(&audience, "grant-audience", "mecatl-execution", "exact grant audience")
	flag.StringVar(&clients, "client-uri-san", "", "comma-separated exact client URI SAN allowlist")
	flag.StringVar(&attesters, "owner-attester-uri-san", "", "comma-separated client identities allowed to attest owners")
	flag.StringVar(&admins, "administrator-uri-san", "", "comma-separated retirement administrators")
	flag.Parse()
	if namespace == "" || clients == "" {
		return errors.New("--namespace and --client-uri-san are required")
	}
	profiles, err := executioncontroller.LoadProfiles(profilesPath)
	if err != nil {
		return err
	}
	serverCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("load provider TLS identity: %w", err)
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return fmt.Errorf("load client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return errors.New("client CA has no certificates")
	}
	priv, err := loadSigningKey(signingPath)
	if err != nil {
		return err
	}
	policies, err := clientPolicies(clients, attesters, admins)
	if err != nil {
		return err
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("build in-cluster Kubernetes config: %w", err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	podexec := executioncontroller.NewPodExecutor(cfg, kube, namespace)
	store := executioncontroller.NewStore(dyn, namespace, profiles, podexec)
	reconciler := executioncontroller.NewReconciler(dyn, kube, namespace, profiles)
	pub := priv.Public().(ed25519.PublicKey)
	verifier := executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{keyID: pub}, Issuer: issuer, Audience: audience, MaxLifetime: 5 * time.Minute}
	handler := executioncontroller.NewHandler(executioncontroller.HandlerConfig{Clients: policies, Signer: executioncontroller.GrantSigner{KeyID: keyID, PrivateKey: priv, Issuer: issuer, Audience: audience, Lifetime: time.Minute}, Verifier: verifier, Ready: reconciler.Ready}, store)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := reconciler.Initialize(ctx); err != nil {
		return fmt.Errorf("initialize controller: %w", err)
	}
	controllerErr := make(chan error, 1)
	go func() { controllerErr <- reconciler.Run(ctx) }()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	tlsLn := tls.NewListener(ln, executioncontroller.TLSConfig(serverCert, pool))
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Minute, IdleTimeout: 60 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(tlsLn) }()
	select {
	case err := <-controllerErr:
		cancel()
		_ = server.Shutdown(context.Background())
		if err != nil {
			return fmt.Errorf("controller: %w", err)
		}
		return nil
	case err := <-serveErr:
		cancel()
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		return server.Shutdown(shutdown)
	}
}
func loadSigningKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load grant signing key: %w", err)
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("grant signing key must be PKCS8 PEM")
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("grant signing key is invalid")
	}
	key, ok := raw.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("grant signing key is not Ed25519")
	}
	return key, nil
}
func clientPolicies(clients, attesters, admins string) (map[string]executioncontroller.ClientPolicy, error) {
	out := map[string]executioncontroller.ClientPolicy{}
	for _, id := range splitList(clients) {
		out[id] = executioncontroller.ClientPolicy{}
	}
	for _, id := range splitList(attesters) {
		p, ok := out[id]
		if !ok {
			return nil, fmt.Errorf("owner attester %q is not a client", id)
		}
		p.MayAttestOwner = true
		out[id] = p
	}
	for _, id := range splitList(admins) {
		p, ok := out[id]
		if !ok {
			return nil, fmt.Errorf("administrator %q is not a client", id)
		}
		p.Administrator = true
		out[id] = p
	}
	return out, nil
}
func splitList(v string) []string {
	out := []string{}
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
