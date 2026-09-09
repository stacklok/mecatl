package microvmmanager

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	microvmclient "github.com/stacklok/mecatl/internal/adapter/microvm"
)

// ReleaseDefaults is the release-generated projection embedded in production
// composition roots.
type ReleaseDefaults struct {
	Version             string `json:"version"`
	Platform            string `json:"platform"`
	URL                 string `json:"url"`
	SHA256              string `json:"sha256"`
	PolicyRevision      string `json:"policy_revision"`
	CertificateIdentity string `json:"certificate_identity"`
	OIDCIssuer          string `json:"oidc_issuer"`
}

// ReadyRequestFromDefaults validates one release-generated defaults projection and
// overlays an optional, already operator-selected guest-egress policy.
func ReadyRequestFromDefaults(encoded, version string, egress ...GuestEgressSelection) (ReadyRequest, error) {
	if encoded == "" {
		return ReadyRequest{}, errors.New("this source-built binary has no authenticated microVM release defaults; install a signed, release-stamped Linux-amd64 mecatl host binary")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return ReadyRequest{}, errors.New("invalid built-in microVM release defaults encoding")
	}
	var byPlatform map[string]ReleaseDefaults
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&byPlatform); err != nil {
		return ReadyRequest{}, fmt.Errorf("decode built-in microVM release defaults: %w", err)
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	defaults := byPlatform[platform]
	if defaults.Version != version || defaults.Platform != platform || defaults.URL == "" || defaults.SHA256 == "" || defaults.PolicyRevision == "" || defaults.CertificateIdentity == "" || defaults.OIDCIssuer == "" {
		return ReadyRequest{}, fmt.Errorf("built-in microVM release defaults do not exactly match version %s on %s", version, platform)
	}
	request := ReadyRequest{
		Release: Release{URL: defaults.URL, SHA256: defaults.SHA256},
		Policy: Policy{
			PolicyRevision:      defaults.PolicyRevision,
			CertificateIdentity: defaults.CertificateIdentity, OIDCIssuer: defaults.OIDCIssuer,
			RequiredAttestations: requiredMicroVMAttestations(),
			GuestEgressMode:      GuestEgressPermissive, Resources: defaultMicroVMResources(),
		},
		PreserveExistingGuestEgress: len(egress) == 0,
	}
	if err := applyGuestEgress(&request, egress); err != nil {
		return ReadyRequest{}, err
	}
	return request, nil
}

func requiredMicroVMAttestations() map[string]string {
	return map[string]string{"runtime": "https://slsa.dev/provenance/v1", "firmware": "https://slsa.dev/provenance/v1", "execution-image": "https://slsa.dev/provenance/v1", "guest-agent": "https://slsa.dev/provenance/v1"}
}

func defaultMicroVMResources() map[string]string {
	return map[string]string{"cpus": "2", "memory": "4GiB"}
}

func applyGuestEgress(request *ReadyRequest, egress []GuestEgressSelection) error {
	if len(egress) > 1 {
		return errors.New("multiple guest egress selections supplied")
	}
	if len(egress) == 1 {
		selection := egress[0]
		if selection.Mode == "" && len(selection.Allow) == 0 {
			selection = NewGuestEgressSelection()
		}
		if err := selection.Validate(); err != nil {
			return err
		}
		request.Policy.GuestEgressMode = selection.Mode
		request.Policy.GuestAllow = append([]EgressRule(nil), selection.Allow...)
	}
	return nil
}

// DefaultLocal builds the user-local manager without changing host state.
func DefaultLocal() (*Manager, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("resolve home for microVM manager: %w", err)
	}
	paths, err := DefaultPaths(HostPaths{
		Home: home, XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"), XDGDataHome: os.Getenv("XDG_DATA_HOME"),
		XDGStateHome: os.Getenv("XDG_STATE_HOME"), XDGRuntimeDir: os.Getenv("XDG_RUNTIME_DIR"), UID: os.Getuid(), GOOS: runtime.GOOS,
	})
	if err != nil {
		return nil, "", err
	}
	endpoint := "unix://" + paths.Socket
	lifecycle, err := microvmclient.New(endpoint)
	if err != nil {
		return nil, "", err
	}
	return New(paths, &DefaultOperations{}, lifecycle), endpoint, nil
}
