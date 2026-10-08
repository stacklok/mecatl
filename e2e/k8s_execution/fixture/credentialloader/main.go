//go:build kind_execution_e2e

// Command credentialloader is the only qualification component permitted to read
// a real provider credential. It creates a new, narrowly labelled Secret and
// never reads Secret data back from Kubernetes.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	namespace          = "execution-qualification"
	secretKey          = "OPENROUTER_API_KEY"
	maxCredentialBytes = 16 << 10
	maxReceiptBytes    = 4 << 10
)

type cleanupReceipt struct {
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	UID        types.UID `json:"uid"`
	Context    string    `json:"context"`
	Kubeconfig string    `json:"kubeconfig"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, errorMessage(err))
		os.Exit(1)
	}
}

func errorMessage(err error) string {
	return "credential loader: " + classify(err)
}

func run(args []string) error {
	if len(args) != 5 {
		return errors.New("usage")
	}
	mode, kubeconfig, contextName, name, receiptPath := args[0], args[1], args[2], args[3], args[4]
	if (mode != "stage" && mode != "delete") || !strings.HasPrefix(name, "mecak8s-live-") || len(utilvalidation.IsDNS1123Subdomain(name)) != 0 {
		return errors.New("invalid arguments")
	}
	kubeconfig, receiptPath, err := validatePrivatePaths(kubeconfig, receiptPath, name)
	if err != nil {
		return err
	}

	// Resolve kube configuration (including any auth plugin) and prove API access
	// before the credential file is opened.
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	).ClientConfig()
	if err != nil {
		return fmt.Errorf("kube configuration: %w", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kube client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = client.Discovery().ServerVersion(); err != nil {
		return fmt.Errorf("kube access: %w", err)
	}

	secrets := client.CoreV1().Secrets(namespace)
	if mode == "delete" {
		if err := deleteStagedSecret(ctx, secrets, receiptPath, kubeconfig, contextName, name); err != nil {
			return err
		}
		fmt.Println(`{"status":"deleted"}`)
		return nil
	}
	path := os.Getenv("MECATL_EXECUTION_CREDENTIAL_FILE")
	credential, err := loadCredential(path)
	if err != nil {
		return err
	}
	defer clear(credential)
	if err := stageSecret(ctx, secrets, receiptPath, kubeconfig, contextName, name, credential); err != nil {
		return err
	}
	fmt.Println(`{"status":"staged","provider":"openrouter"}`)
	return nil
}

func stageSecret(ctx context.Context, secrets v1.SecretInterface, receiptPath, kubeconfig, contextName, name string, credential []byte) error {
	created, err := secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "mecatl-execution-live-qualification"}},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{secretKey: credential},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("secret stage: %w", err)
	}
	if created == nil || created.UID == "" {
		return errors.New("secret staged without UID; cleanup pending")
	}
	receipt := cleanupReceipt{Name: name, Namespace: namespace, UID: created.UID, Context: contextName, Kubeconfig: kubeconfig}
	if err := writeReceipt(receiptPath, receipt); err != nil {
		uid := receipt.UID
		_ = secrets.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		return errors.New("cleanup receipt creation failed; cleanup status ambiguous")
	}
	return nil
}

func deleteStagedSecret(ctx context.Context, secrets v1.SecretInterface, receiptPath, kubeconfig, contextName, name string) error {
	receipt, err := readReceipt(receiptPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cleanup receipt: %w", err)
	}
	if receipt.Name != name || receipt.Namespace != namespace || receipt.Context != contextName || receipt.Kubeconfig != kubeconfig {
		return errors.New("cleanup receipt identity mismatch")
	}
	if receipt.UID == "" {
		return errors.New("secret staged without UID; cleanup pending")
	}
	uid := receipt.UID
	err = secrets.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("secret cleanup: %w", err)
	}
	if err := os.Remove(receiptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cleanup receipt removal failed")
	}
	return nil
}

func validatePrivatePaths(kubeconfig, receiptPath, name string) (string, string, error) {
	if !filepath.IsAbs(kubeconfig) || !filepath.IsAbs(receiptPath) {
		return "", "", errors.New("state paths must be absolute")
	}
	kubeconfig, err := filepath.EvalSymlinks(kubeconfig)
	if err != nil {
		return "", "", errors.New("kubeconfig unavailable")
	}
	kubeInfo, err := os.Stat(kubeconfig)
	if err != nil || !kubeInfo.Mode().IsRegular() || kubeInfo.Mode().Perm()&0o077 != 0 {
		return "", "", errors.New("kubeconfig must be a private regular file")
	}
	stateDir, err := filepath.EvalSymlinks(filepath.Dir(receiptPath))
	if err != nil || stateDir != filepath.Dir(kubeconfig) {
		return "", "", errors.New("receipt path must be in kubeconfig state directory")
	}
	info, err := os.Stat(stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", "", errors.New("state directory must be private")
	}
	base := filepath.Base(receiptPath)
	if base != "live-secret-"+name+".receipt.json" || filepath.Clean(receiptPath) != filepath.Join(stateDir, base) {
		return "", "", errors.New("invalid receipt path")
	}
	return kubeconfig, filepath.Join(stateDir, base), nil
}

func writeReceipt(path string, receipt cleanupReceipt) error {
	blob, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(append(blob, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func readReceipt(path string) (cleanupReceipt, error) {
	var receipt cleanupReceipt
	f, err := os.Open(path)
	if err != nil {
		return receipt, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxReceiptBytes {
		return receipt, errors.New("receipt must be a bounded private regular file")
	}
	dec := json.NewDecoder(io.LimitReader(f, maxReceiptBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&receipt); err != nil {
		return receipt, errors.New("invalid cleanup receipt")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return receipt, errors.New("invalid cleanup receipt")
	}
	return receipt, nil
}

func loadCredential(path string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("credential file must be an absolute path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("credential file unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("credential file must be regular")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("credential file permissions must exclude group and others")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil {
		return nil, errors.New("credential file read failed")
	}
	defer clear(b)
	if len(b) > maxCredentialBytes {
		return nil, errors.New("credential file exceeds limit")
	}
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return nil, errors.New("credential file is empty")
	}
	return append([]byte(nil), trimmed...), nil
}

func classify(err error) string {
	s := err.Error()
	for _, allowed := range []string{
		"usage", "invalid arguments", "state paths must be absolute", "kubeconfig unavailable", "kubeconfig must be a private regular file",
		"receipt path must be in kubeconfig state directory", "state directory must be private", "invalid receipt path",
		"credential file must be an absolute path", "credential file unavailable", "credential file must be regular",
		"credential file permissions must exclude group and others", "credential file read failed", "credential file exceeds limit", "credential file is empty",
		"cleanup receipt creation failed; cleanup status ambiguous", "secret staged without UID; cleanup pending", "cleanup receipt identity mismatch",
	} {
		if s == allowed {
			return allowed
		}
	}
	return "operation failed (details redacted)"
}
