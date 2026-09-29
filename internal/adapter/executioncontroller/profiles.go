package executioncontroller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const maxTolerationSeconds int64 = 86400

// ProfilesFile is the strict operator profile-file schema.
type ProfilesFile struct {
	Profiles map[string]ProfileSpec `yaml:"profiles"`
}

// ProfileSpec defines immutable image, storage, resource, and operation bounds.
type ProfileSpec struct {
	Image                   string              `yaml:"image"`
	StorageClass            string              `yaml:"storageClass"`
	StorageSize             string              `yaml:"storageSize"`
	CPURequest              string              `yaml:"cpuRequest"`
	MemoryRequest           string              `yaml:"memoryRequest"`
	CPULimit                string              `yaml:"cpuLimit"`
	MemoryLimit             string              `yaml:"memoryLimit"`
	EphemeralStorageRequest string              `yaml:"ephemeralStorageRequest"`
	EphemeralStorageLimit   string              `yaml:"ephemeralStorageLimit"`
	TmpSizeLimit            string              `yaml:"tmpSizeLimit"`
	RuntimeClassName        string              `yaml:"runtimeClassName"`
	ImagePullSecrets        []string            `yaml:"imagePullSecrets,omitempty"`
	NodeSelector            map[string]string   `yaml:"nodeSelector,omitempty"`
	Tolerations             []corev1.Toleration `yaml:"tolerations,omitempty"`
	MaxFileBytes            int64               `yaml:"maxFileBytes"`
	MaxCommandBytes         int64               `yaml:"maxCommandBytes"`
	MaxCommandDuration      time.Duration       `yaml:"maxCommandDuration"`
	MaxEnvironments         int                 `yaml:"maxEnvironments"`
}

// Profiles is a validated immutable profile registry.
type Profiles struct {
	byName                 map[string]resolvedProfile
	executorServiceAccount string
}

// WithExecutorServiceAccount sets the chart-owned identity for Pod construction and validation.
func (p *Profiles) WithExecutorServiceAccount(name string) *Profiles {
	p.executorServiceAccount = name
	return p
}

type resolvedProfile struct {
	Spec                    ProfileSpec
	Digest                  string
	StorageSize             resource.Quantity
	CPURequest              resource.Quantity
	MemoryRequest           resource.Quantity
	CPULimit                resource.Quantity
	MemoryLimit             resource.Quantity
	EphemeralStorageRequest resource.Quantity
	EphemeralStorageLimit   resource.Quantity
	TmpSizeLimit            resource.Quantity
}

// LoadProfiles reads and strictly validates an operator profile file.
func LoadProfiles(path string) (*Profiles, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profiles: %w", err)
	}
	var f ProfilesFile
	if err := yaml.UnmarshalWithOptions(b, &f, yaml.DisallowUnknownField()); err != nil {
		return nil, fmt.Errorf("decode profiles: %w", err)
	}
	if len(f.Profiles) == 0 {
		return nil, errors.New("profiles file has no profiles")
	}
	p := &Profiles{byName: map[string]resolvedProfile{}}
	for name, s := range f.Profiles {
		quantities, err := validateProfile(name, s)
		if err != nil {
			return nil, err
		}
		canonical, err := yaml.Marshal(s)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(canonical)
		quantities.Spec = s
		quantities.Digest = "sha256:" + hex.EncodeToString(sum[:])
		p.byName[name] = quantities
	}
	return p, nil
}
func validateProfile(name string, s ProfileSpec) (resolvedProfile, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return resolvedProfile{}, fmt.Errorf("invalid profile name %q", name)
	}
	if !strings.Contains(s.Image, "@sha256:") {
		return resolvedProfile{}, fmt.Errorf("profile %q image must be digest-pinned", name)
	}
	if s.StorageClass == "" || s.StorageSize == "" {
		return resolvedProfile{}, fmt.Errorf("profile %q requires explicit storage class and size", name)
	}
	if s.CPURequest == "" || s.MemoryRequest == "" || s.CPULimit == "" || s.MemoryLimit == "" || s.EphemeralStorageRequest == "" || s.EphemeralStorageLimit == "" || s.TmpSizeLimit == "" || s.RuntimeClassName == "" {
		return resolvedProfile{}, fmt.Errorf("profile %q requires explicit resource requests, limits, tmp size, and runtime class", name)
	}
	quantities, err := resolveProfileQuantities(name, s)
	if err != nil {
		return resolvedProfile{}, err
	}
	if !validExecutionBounds(s) {
		return resolvedProfile{}, fmt.Errorf("profile %q has invalid execution bounds", name)
	}
	if err := validateImagePullSecrets(name, s.ImagePullSecrets); err != nil {
		return resolvedProfile{}, err
	}
	if err := validateScheduling(name, s.NodeSelector, s.Tolerations); err != nil {
		return resolvedProfile{}, err
	}
	return quantities, nil
}

func validateScheduling(name string, nodeSelector map[string]string, tolerations []corev1.Toleration) error {
	if len(nodeSelector) > 32 {
		return fmt.Errorf("profile %q has too many nodeSelector entries", name)
	}
	for key, value := range nodeSelector {
		if len(validation.IsQualifiedName(key)) != 0 || len(validation.IsValidLabelValue(value)) != 0 {
			return fmt.Errorf("profile %q has invalid nodeSelector %q", name, key)
		}
	}
	if len(tolerations) > 16 {
		return fmt.Errorf("profile %q has too many tolerations", name)
	}
	for i, toleration := range tolerations {
		if err := validateToleration(toleration); err != nil {
			return fmt.Errorf("profile %q toleration %d: %w", name, i, err)
		}
		for _, previous := range tolerations[:i] {
			if reflect.DeepEqual(previous, toleration) {
				return fmt.Errorf("profile %q has duplicate toleration", name)
			}
		}
	}
	return nil
}

func validateToleration(t corev1.Toleration) error {
	if t.Key != "" && len(validation.IsQualifiedName(t.Key)) != 0 {
		return fmt.Errorf("invalid key %q", t.Key)
	}
	if t.Operator != corev1.TolerationOpExists && t.Operator != corev1.TolerationOpEqual {
		return fmt.Errorf("invalid operator %q", t.Operator)
	}
	if t.Operator == corev1.TolerationOpExists && t.Value != "" {
		return errors.New("exists operator requires an empty value")
	}
	if t.Operator == corev1.TolerationOpEqual && t.Key == "" {
		return errors.New("equal operator requires a key")
	}
	if t.Operator == corev1.TolerationOpEqual && len(validation.IsValidLabelValue(t.Value)) != 0 {
		return fmt.Errorf("invalid value %q", t.Value)
	}
	if t.Effect != "" && t.Effect != corev1.TaintEffectNoSchedule && t.Effect != corev1.TaintEffectPreferNoSchedule && t.Effect != corev1.TaintEffectNoExecute {
		return fmt.Errorf("invalid effect %q", t.Effect)
	}
	if t.TolerationSeconds != nil && (*t.TolerationSeconds < 0 || *t.TolerationSeconds > maxTolerationSeconds || t.Effect != corev1.TaintEffectNoExecute) {
		return fmt.Errorf("tolerationSeconds must be between 0 and %d and requires the NoExecute effect", maxTolerationSeconds)
	}
	return nil
}

func validateImagePullSecrets(name string, secrets []string) error {
	if len(secrets) > 8 {
		return fmt.Errorf("profile %q has too many imagePullSecrets", name)
	}
	seen := make(map[string]bool, len(secrets))
	for _, secret := range secrets {
		if len(validation.IsDNS1123Subdomain(secret)) != 0 || seen[secret] {
			return fmt.Errorf("profile %q has invalid or duplicate imagePullSecret %q", name, secret)
		}
		for _, label := range strings.Split(secret, ".") {
			if len(validation.IsDNS1123Label(label)) != 0 {
				return fmt.Errorf("profile %q has invalid imagePullSecret %q", name, secret)
			}
		}
		seen[secret] = true
	}
	return nil
}

func resolveProfileQuantities(name string, s ProfileSpec) (resolvedProfile, error) {
	storage, err := positiveQuantity(name, "storageSize", s.StorageSize)
	if err != nil {
		return resolvedProfile{}, err
	}
	cpuRequest, err := positiveQuantity(name, "cpuRequest", s.CPURequest)
	if err != nil {
		return resolvedProfile{}, err
	}
	memoryRequest, err := positiveQuantity(name, "memoryRequest", s.MemoryRequest)
	if err != nil {
		return resolvedProfile{}, err
	}
	cpuLimit, err := positiveQuantity(name, "cpuLimit", s.CPULimit)
	if err != nil {
		return resolvedProfile{}, err
	}
	memoryLimit, err := positiveQuantity(name, "memoryLimit", s.MemoryLimit)
	if err != nil {
		return resolvedProfile{}, err
	}
	ephemeralRequest, err := positiveQuantity(name, "ephemeralStorageRequest", s.EphemeralStorageRequest)
	if err != nil {
		return resolvedProfile{}, err
	}
	ephemeralLimit, err := positiveQuantity(name, "ephemeralStorageLimit", s.EphemeralStorageLimit)
	if err != nil {
		return resolvedProfile{}, err
	}
	tmpLimit, err := positiveQuantity(name, "tmpSizeLimit", s.TmpSizeLimit)
	if err != nil {
		return resolvedProfile{}, err
	}
	if cpuRequest.Cmp(cpuLimit) > 0 {
		return resolvedProfile{}, fmt.Errorf("profile %q cpuRequest exceeds cpuLimit", name)
	}
	if memoryRequest.Cmp(memoryLimit) > 0 {
		return resolvedProfile{}, fmt.Errorf("profile %q memoryRequest exceeds memoryLimit", name)
	}
	if ephemeralRequest.Cmp(ephemeralLimit) > 0 || tmpLimit.Cmp(ephemeralLimit) > 0 {
		return resolvedProfile{}, fmt.Errorf("profile %q ephemeral storage request or tmp limit exceeds ephemeral storage limit", name)
	}
	return resolvedProfile{StorageSize: storage, CPURequest: cpuRequest, MemoryRequest: memoryRequest, CPULimit: cpuLimit, MemoryLimit: memoryLimit, EphemeralStorageRequest: ephemeralRequest, EphemeralStorageLimit: ephemeralLimit, TmpSizeLimit: tmpLimit}, nil
}

func validExecutionBounds(s ProfileSpec) bool {
	return s.MaxFileBytes > 0 && s.MaxFileBytes <= 5<<20 &&
		s.MaxCommandBytes > 0 && s.MaxCommandBytes <= 1<<20 &&
		s.MaxCommandDuration > 0 && s.MaxCommandDuration <= 30*time.Minute &&
		s.MaxEnvironments > 0 && s.MaxEnvironments <= 10_000
}

func positiveQuantity(profile, field, value string) (resource.Quantity, error) {
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("profile %q %s is invalid: %w", profile, field, err)
	}
	if quantity.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("profile %q %s must be positive", profile, field)
	}
	return quantity, nil
}
func (p *Profiles) get(name string) (resolvedProfile, bool) {
	if p == nil {
		return resolvedProfile{}, false
	}
	v, ok := p.byName[name]
	return v, ok
}

func (p *Profiles) clusterResources() (runtimeClasses, storageClasses []string) {
	if p == nil {
		return nil, nil
	}
	runtimes := make(map[string]struct{}, len(p.byName))
	storage := make(map[string]struct{}, len(p.byName))
	for _, profile := range p.byName {
		runtimes[profile.Spec.RuntimeClassName] = struct{}{}
		storage[profile.Spec.StorageClass] = struct{}{}
	}
	for name := range runtimes {
		runtimeClasses = append(runtimeClasses, name)
	}
	for name := range storage {
		storageClasses = append(storageClasses, name)
	}
	sort.Strings(runtimeClasses)
	sort.Strings(storageClasses)
	return runtimeClasses, storageClasses
}
