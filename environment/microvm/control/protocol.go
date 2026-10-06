package control

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// ProtocolVersion is the only guest protocol version accepted by this release.
	ProtocolVersion uint16 = 1
	// DefaultMaxMessageBytes is the hard upper bound negotiated for protocol messages.
	DefaultMaxMessageBytes uint32 = 1 << 20
)

var (
	// ErrCapabilityMismatch reports a missing mandatory guest capability.
	ErrCapabilityMismatch = errors.New("microvm guest capability mismatch")
	// ErrMessageBound reports a missing or invalid negotiated message bound.
	ErrMessageBound = errors.New("microvm guest message bound is missing")
	// ErrProtocolVersion reports an incompatible guest protocol version.
	ErrProtocolVersion = errors.New("microvm guest protocol version mismatch")
	// ErrFrameTooLarge reports a frame larger than the codec's immutable bound.
	ErrFrameTooLarge = errors.New("microvm protocol frame exceeds bound")
	// ErrMalformedFrame reports invalid bounded protocol framing or JSON.
	ErrMalformedFrame = errors.New("malformed microvm protocol frame")
)

// Capability is a guest service property required before any operation is sent.
type Capability string

const (
	// CapabilityFilesystem requires version-aware guest filesystem operations.
	CapabilityFilesystem Capability = "filesystem"
	// CapabilityStreaming requires ordered output streaming.
	CapabilityStreaming Capability = "streaming"
	// CapabilityCancellation requires guest process-group cancellation.
	CapabilityCancellation Capability = "cancellation"
	// CapabilityGenerationBinding requires every operation to bind one generation.
	CapabilityGenerationBinding Capability = "generation-binding"
	// CapabilityMessageBound requires a negotiated maximum message size.
	CapabilityMessageBound Capability = "message-bound"
)

// Capabilities is a deterministic capability list.
type Capabilities []Capability

// RequiredCapabilities returns a fresh list of all mandatory version-1 capabilities.
func RequiredCapabilities() Capabilities {
	return Capabilities{
		CapabilityFilesystem,
		CapabilityStreaming,
		CapabilityCancellation,
		CapabilityGenerationBinding,
		CapabilityMessageBound,
	}
}

// Without returns a copy excluding capability.
func (c Capabilities) Without(capability Capability) Capabilities {
	result := make(Capabilities, 0, len(c))
	for _, candidate := range c {
		if candidate != capability {
			result = append(result, candidate)
		}
	}
	return result
}

func (c Capabilities) contains(capability Capability) bool {
	for _, candidate := range c {
		if candidate == capability {
			return true
		}
	}
	return false
}

// Agreement is the capability set and bound selected for a connection.
type Agreement struct {
	Version         uint16       `json:"version"`
	Capabilities    Capabilities `json:"capabilities"`
	MaxMessageBytes uint32       `json:"max_message_bytes"`
}

// Codec reads and writes unsigned 32-bit big-endian length-prefixed JSON frames.
type Codec struct {
	max uint32
}

// NewCodec creates a codec with an immutable per-frame byte bound.
func NewCodec(maxMessageBytes uint32) Codec {
	return Codec{max: maxMessageBytes}
}

// Write emits exactly one bounded frame.
func (c Codec) Write(dst io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode microvm protocol frame: %w", err)
	}
	if len(payload) == 0 || uint64(len(payload)) > uint64(c.max) {
		return ErrFrameTooLarge
	}
	var header [4]byte
	// The payload was bounded by a uint32-valued maximum above.
	binary.BigEndian.PutUint32(header[:], uint32(len(payload))) //nolint:gosec // proven in the preceding check
	frame := make([]byte, len(header)+len(payload))
	copy(frame, header[:])
	copy(frame[len(header):], payload)
	if err := writeFull(dst, frame); err != nil {
		return fmt.Errorf("write microvm protocol frame: %w", err)
	}
	return nil
}

func writeFull(dst io.Writer, frame []byte) error {
	for len(frame) > 0 {
		written, err := dst.Write(frame)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(frame) {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

// Read receives exactly one bounded frame and rejects trailing JSON values.
func (c Codec) Read(src io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		return fmt.Errorf("read microvm protocol frame header: %w", err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return ErrMalformedFrame
	}
	if size > c.max {
		return ErrFrameTooLarge
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(src, payload); err != nil {
		return fmt.Errorf("read microvm protocol frame body: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedFrame, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrMalformedFrame
	}
	return nil
}

func validateAgreement(agreement Agreement, version uint16, capabilities Capabilities, maxMessageBytes uint32) error {
	if version != ProtocolVersion || agreement.Version != ProtocolVersion {
		return ErrProtocolVersion
	}
	for _, capability := range RequiredCapabilities() {
		if !capabilities.contains(capability) || !agreement.Capabilities.contains(capability) {
			return fmt.Errorf("%w: missing %s", ErrCapabilityMismatch, capability)
		}
	}
	if maxMessageBytes == 0 || agreement.MaxMessageBytes == 0 || agreement.MaxMessageBytes > maxMessageBytes || agreement.MaxMessageBytes > DefaultMaxMessageBytes {
		return ErrMessageBound
	}
	return nil
}
