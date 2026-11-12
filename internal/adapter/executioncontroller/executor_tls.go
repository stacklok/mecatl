package executioncontroller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/stacklok/mecatl/engine/port"
)

const (
	serverTLSVolume         = "server-tls"
	serverTLSMountPath      = "/etc/mecatl-executor/tls"
	providerTrustVolume     = "provider-trust"
	providerTrustMountPath  = "/etc/mecatl-executor/provider"
	providerTrustCAFile     = "ca.crt"
	providerTrustURIKey     = "client-uri"
	executorTLSLabel        = "execution.mecatl.dev/executor-tls"
	serverCertificateField  = "serverCertificate"
	executorCertificateLife = 10 * 365 * 24 * time.Hour
)

// ExecutorTrust names the sources executor Pods mount to authenticate the
// provider's client certificate: a CA bundle key in a Secret and a ConfigMap
// holding the provider's URI SAN under client-uri.
type ExecutorTrust struct {
	CASecret        string
	CAKey           string
	ClientConfigMap string
}

func (t ExecutorTrust) valid() bool {
	return len(validation.IsDNS1123Subdomain(t.CASecret)) == 0 &&
		len(validation.IsConfigMapKey(t.CAKey)) == 0 &&
		len(validation.IsDNS1123Subdomain(t.ClientConfigMap)) == 0
}

var errServerCertificatePruned = errors.New("status.serverCertificate was pruned; " +
	"apply the ExecutionEnvironment CRD from this release")

type serverCertificatePin struct {
	SecretName  string
	Fingerprint string
	PodUID      string
}

func readServerCertificatePin(env *unstructured.Unstructured) serverCertificatePin {
	return serverCertificatePin{
		SecretName:  textNested(env.Object, statusField, serverCertificateField, "secretName"),
		Fingerprint: textNested(env.Object, statusField, serverCertificateField, "fingerprint"),
		PodUID:      textNested(env.Object, statusField, serverCertificateField, "podUID"),
	}
}

func writeServerCertificatePin(o *unstructured.Unstructured, pin serverCertificatePin) {
	value := map[string]any{"secretName": pin.SecretName, "fingerprint": pin.Fingerprint}
	if pin.PodUID != "" {
		value["podUID"] = pin.PodUID
	}
	_ = unstructured.SetNestedMap(o.Object, value, statusField, serverCertificateField)
}

func certificateFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func executorTLSSecretName(env, fingerprint string) string {
	return resourceName("executor-tls", env) + "-" + fingerprint[:16]
}

func generateExecutorCertificate(
	env string, now time.Time,
) (certPEM, keyPEM []byte, fingerprint string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, "", err
	}
	// The provider pins the fingerprint, so validity is not a trust input.
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: env},
		URIs:         []*url.URL{{Scheme: "urn", Opaque: "mecatl:execution-environment:" + env}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(executorCertificateLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, "", err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, certificateFingerprint(der), nil
}

func secretFingerprint(secret *corev1.Secret) string {
	block, _ := pem.Decode(secret.Data[corev1.TLSCertKey])
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	return certificateFingerprint(block.Bytes)
}

func executorSecretBelongs(env *unstructured.Unstructured, secret *corev1.Secret) bool {
	return secret.Labels["execution.mecatl.dev/environment"] == env.GetName() &&
		secret.Labels["execution.mecatl.dev/allocation-uid"] == string(env.GetUID()) &&
		secret.Labels[executorTLSLabel] == "true"
}

// ensureServerCertificate returns a pin whose Secret exists, issuing a new
// certificate when no Pod-less pin is reusable. Must only run with no executor Pod.
func (r *Reconciler) ensureServerCertificate(
	ctx context.Context, env *unstructured.Unstructured,
) (serverCertificatePin, error) {
	current := readServerCertificatePin(env)
	if current.PodUID != "" {
		return serverCertificatePin{}, errors.New(
			"executor certificate is pinned to a previous Pod")
	}
	secrets := r.kube.CoreV1().Secrets(r.namespace)
	if current.SecretName != "" {
		secret, err := secrets.Get(ctx, current.SecretName, metav1.GetOptions{})
		if err == nil {
			if !executorSecretBelongs(env, secret) ||
				secret.Immutable == nil || !*secret.Immutable ||
				secretFingerprint(secret) != current.Fingerprint {
				return serverCertificatePin{}, errors.New(
					"pinned executor certificate Secret does not match its pin")
			}
			return current, nil
		}
		if !apierrors.IsNotFound(err) {
			return serverCertificatePin{}, err
		}
	}
	certPEM, keyPEM, fingerprint, err := generateExecutorCertificate(env.GetName(), r.now())
	if err != nil {
		return serverCertificatePin{}, err
	}
	next := serverCertificatePin{
		SecretName:  executorTLSSecretName(env.GetName(), fingerprint),
		Fingerprint: fingerprint,
	}
	immutable, controller := true, false
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: next.SecretName,
			Labels: map[string]string{
				"execution.mecatl.dev/environment":    env.GetName(),
				"execution.mecatl.dev/allocation-uid": string(env.GetUID()),
				executorTLSLabel:                      "true",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: env.GetAPIVersion(),
				Kind:       env.GetKind(),
				Name:       env.GetName(),
				UID:        env.GetUID(),
				Controller: &controller,
			}},
		},
		Type:      corev1.SecretTypeTLS,
		Immutable: &immutable,
		Data:      map[string][]byte{corev1.TLSCertKey: certPEM, corev1.TLSPrivateKeyKey: keyPEM},
	}
	created, err := secrets.Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return serverCertificatePin{}, err
	}
	stored, err := r.updateStoredStatus(ctx, env, func(o *unstructured.Unstructured) error {
		if !runtimeObservationMatches(o, env) || readServerCertificatePin(o) != current {
			return lifecycleConflict()
		}
		writeServerCertificatePin(o, next)
		return nil
	})
	if err == nil && readServerCertificatePin(stored) != next {
		err = errServerCertificatePruned
		r.diagnostics.Log(ctx, port.LevelError, "executor certificate pin was not stored",
			"environment", env.GetName(), "error", err.Error())
	}
	if err != nil {
		uid := created.UID
		options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
		deleteErr := secrets.Delete(ctx, created.Name, options)
		if deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			r.diagnostics.Log(ctx, port.LevelWarn,
				"unpinned executor certificate Secret not removed",
				"environment", env.GetName(), "secret", created.Name)
		}
		return serverCertificatePin{}, err
	}
	if current.SecretName != "" {
		r.diagnostics.Log(ctx, port.LevelInfo, "executor certificate reissued",
			"environment", env.GetName(), "previousFingerprint", current.Fingerprint)
	}
	r.diagnostics.Log(ctx, port.LevelInfo, "executor certificate pinned",
		"environment", env.GetName(), "secret", next.SecretName, "fingerprint", next.Fingerprint)
	return next, nil
}

func (r *Reconciler) recordPinnedPod(
	ctx context.Context, env *unstructured.Unstructured, pin serverCertificatePin, pod *corev1.Pod,
) error {
	if pin.PodUID == string(pod.UID) {
		return nil
	}
	if pin.PodUID != "" {
		return errors.New("executor certificate is pinned to a different Pod")
	}
	return r.updateStatus(ctx, env, func(o *unstructured.Unstructured) error {
		if !runtimeObservationMatches(o, env) || readServerCertificatePin(o) != pin {
			return lifecycleConflict()
		}
		pin.PodUID = string(pod.UID)
		writeServerCertificatePin(o, pin)
		return nil
	})
}

// deleteServerCertificate removes the pinned Secret once its Pod is gone.
func (r *Reconciler) deleteServerCertificate(
	ctx context.Context, env *unstructured.Unstructured,
) error {
	pin := readServerCertificatePin(env)
	if pin.SecretName == "" {
		return nil
	}
	secrets := r.kube.CoreV1().Secrets(r.namespace)
	secret, err := secrets.Get(ctx, pin.SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !executorSecretBelongs(env, secret) {
		return errors.New("pinned executor certificate Secret has foreign ownership")
	}
	uid := secret.UID
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
	if err := secrets.Delete(ctx, secret.Name, options); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func executorTLSVolumes(secretName string, trust ExecutorTrust) []corev1.Volume {
	secretMode, projectedMode := int32(0o440), int32(0o444)
	caSource := corev1.VolumeProjection{Secret: &corev1.SecretProjection{
		LocalObjectReference: corev1.LocalObjectReference{Name: trust.CASecret},
		Items:                []corev1.KeyToPath{{Key: trust.CAKey, Path: providerTrustCAFile}},
	}}
	uriSource := corev1.VolumeProjection{ConfigMap: &corev1.ConfigMapProjection{
		LocalObjectReference: corev1.LocalObjectReference{Name: trust.ClientConfigMap},
		Items: []corev1.KeyToPath{
			{Key: providerTrustURIKey, Path: providerTrustURIKey},
		},
	}}
	return []corev1.Volume{
		{
			Name: serverTLSVolume,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName:  secretName,
				DefaultMode: &secretMode,
			}},
		},
		{
			Name: providerTrustVolume,
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				DefaultMode: &projectedMode,
				Sources:     []corev1.VolumeProjection{caSource, uriSource},
			}},
		},
	}
}

func executorVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: "workspace", MountPath: "/workspace"},
		{Name: "tmp", MountPath: "/tmp"},
		{Name: serverTLSVolume, MountPath: serverTLSMountPath, ReadOnly: true},
		{Name: providerTrustVolume, MountPath: providerTrustMountPath, ReadOnly: true},
	}
}

func validExecutorTLSVolumes(got []corev1.Volume, secretName string, trust ExecutorTrust) bool {
	return secretName != "" && reflect.DeepEqual(got, executorTLSVolumes(secretName, trust))
}
