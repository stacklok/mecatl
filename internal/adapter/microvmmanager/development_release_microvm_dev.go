//go:build microvm_dev

package microvmmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// DevelopmentReleaseSchema identifies the local development descriptor format.
const DevelopmentReleaseSchema = "mecatl-microvm-development-release/v1"

// DevelopmentReleaseDescriptor is the deliberately narrow local equivalent of
// release-stamped bootstrap defaults. It is available only in microvm_dev builds.
type DevelopmentReleaseDescriptor struct {
	Schema              string `json:"schema"`
	Platform            string `json:"platform"`
	SourceBuildIdentity string `json:"source_build_identity"`
	BundlePath          string `json:"bundle_path"`
	BundleSHA256        string `json:"bundle_sha256"`
	PublicKeyPath       string `json:"public_key_path"`
	PublicKeyIdentity   string `json:"public_key_identity"`
	PolicyRevision      string `json:"policy_revision"`
}

// ReadyRequestFromDevelopmentDescriptor validates local bootstrap inputs before
// returning the same readiness request consumed by the production manager.
func ReadyRequestFromDevelopmentDescriptor(path, sourceBuildIdentity string, egress ...GuestEgressSelection) (ReadyRequest, error) {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if !supportedPlatform(runtime.GOOS, runtime.GOARCH) {
		return ReadyRequest{}, fmt.Errorf("microVM development releases are unsupported on %s", platform)
	}
	data, err := readOwnerOnlyRegular(path, 1<<20)
	if err != nil {
		return ReadyRequest{}, fmt.Errorf("read microVM development release descriptor: %w", err)
	}
	var descriptor DevelopmentReleaseDescriptor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return ReadyRequest{}, fmt.Errorf("decode microVM development release descriptor: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ReadyRequest{}, errors.New("microVM development release descriptor must contain exactly one JSON object")
	}
	if descriptor.Schema != DevelopmentReleaseSchema || descriptor.Platform != platform || descriptor.SourceBuildIdentity == "" || descriptor.PolicyRevision == "" {
		return ReadyRequest{}, errors.New("microVM development release descriptor identity is invalid")
	}
	if descriptor.SourceBuildIdentity != sourceBuildIdentity {
		return ReadyRequest{}, errors.New("microVM development release descriptor does not match this source build; after source changes, rerun task microvm:dev:prepare and task microvm:dev:build")
	}
	if !lowerSHA256(descriptor.BundleSHA256) || !strings.HasPrefix(descriptor.PublicKeyIdentity, "sha256:") || !lowerSHA256(strings.TrimPrefix(descriptor.PublicKeyIdentity, "sha256:")) {
		return ReadyRequest{}, errors.New("microVM development release descriptor contains an invalid digest identity")
	}
	bundle, err := openOwnerOnlyRegular(descriptor.BundlePath, maxReleaseBundleBytes)
	if err != nil {
		return ReadyRequest{}, fmt.Errorf("validate local microVM release bundle: %w", err)
	}
	keyFile, err := openOwnerOnlyRegular(descriptor.PublicKeyPath, 1<<20)
	if err != nil {
		_ = bundle.Close()
		return ReadyRequest{}, fmt.Errorf("validate local microVM release public key: %w", err)
	}
	key, err := readBoundedFile(keyFile, 1<<20)
	_ = keyFile.Close()
	if err != nil {
		_ = bundle.Close()
		return ReadyRequest{}, fmt.Errorf("read local microVM release public key: %w", err)
	}
	if "sha256:"+bytesSHA256Hex(key) != descriptor.PublicKeyIdentity {
		_ = bundle.Close()
		return ReadyRequest{}, errors.New("local microVM release public-key identity mismatch")
	}
	bundleDigest, err := openedFileSHA256(bundle, maxReleaseBundleBytes)
	if err != nil {
		_ = bundle.Close()
		return ReadyRequest{}, fmt.Errorf("identify local microVM release bundle: %w", err)
	}
	if bundleDigest != descriptor.BundleSHA256 {
		_ = bundle.Close()
		return ReadyRequest{}, errors.New("local microVM release bundle SHA-256 mismatch")
	}
	request := ReadyRequest{
		Release: Release{bundlePath: filepath.Clean(descriptor.BundlePath), bundleFile: bundle, SHA256: descriptor.BundleSHA256},
		Policy: Policy{
			PolicyRevision: descriptor.PolicyRevision, PublicKeyIdentity: descriptor.PublicKeyIdentity, publicKey: key,
			RequiredAttestations: requiredMicroVMAttestations(), GuestEgressMode: GuestEgressPermissive,
		},
	}
	if err := applyGuestEgress(&request, egress); err != nil {
		_ = bundle.Close()
		return ReadyRequest{}, err
	}
	return request, nil
}

func readOwnerOnlyRegular(path string, maxBytes int64) ([]byte, error) {
	file, err := openOwnerOnlyRegular(path, maxBytes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return readBoundedFile(file, maxBytes)
}

func openOwnerOnlyRegular(path string, maxBytes int64) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("path must be absolute and clean")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0) // #nosec G304 -- local development input is opened without following the final symlink.
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if int64(stat.Uid) != int64(os.Getuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 0 || info.Size() > maxBytes {
		_ = file.Close()
		return nil, errors.New("path must be an owner-only regular non-symlink file owned by the current user")
	}
	return file, nil
}

func readBoundedFile(file *os.File, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}

func openedFileSHA256(file *os.File, maxBytes int64) (string, error) {
	hash := sha256.New()
	written, err := io.Copy(hash, io.NewSectionReader(file, 0, maxBytes+1))
	if err != nil {
		return "", err
	}
	if written > maxBytes {
		return "", errors.New("file exceeds size limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func lowerSHA256(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func bytesSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
