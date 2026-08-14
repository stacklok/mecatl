package credentialstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	envelopeHeaderBytes = 16
	envelopeNonceBytes  = 12
	envelopeTagBytes    = 16
	// MaxEnvelopeBytes is the largest accepted persisted record.
	MaxEnvelopeBytes = envelopeHeaderBytes + envelopeNonceBytes + envelopeTagBytes + MaxValueBytes

	envelopeVersion   = byte(1)
	envelopeAlgorithm = byte(1)
	envelopeFlags     = byte(0)
	aadDomain         = "mecatl/credentialstore/aad/v1"
)

var envelopeMagic = [8]byte{'M', 'E', 'C', 'A', 'T', 'L', 'C', 0}

func sealEnvelope(key, namespace, recordKey, value []byte, random io.Reader) ([]byte, Version, error) {
	if random == nil {
		random = rand.Reader
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, Version{}, fmt.Errorf("seal credential envelope: %w", ErrInvalidKey)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, Version{}, fmt.Errorf("seal credential envelope: %w", ErrUnavailable)
	}
	var header [envelopeHeaderBytes]byte
	copy(header[:8], envelopeMagic[:])
	header[8], header[9], header[10], header[11] = envelopeVersion, envelopeAlgorithm, envelopeNonceBytes, envelopeFlags
	binary.BigEndian.PutUint32(header[12:16], uint32(len(value)+envelopeTagBytes)) // #nosec G115 -- value is bounded to one MiB.
	nonce := make([]byte, envelopeNonceBytes)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, Version{}, fmt.Errorf("seal credential envelope: %w", ErrUnavailable)
	}
	aad := envelopeAAD(header[:], namespace, recordKey)
	out := make([]byte, 0, MaxEnvelopeBytes)
	out = append(out, header[:]...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, value, aad)
	return out, versionFromEnvelope(out), nil
}

func openEnvelope(key, namespace, recordKey, envelope []byte) ([]byte, Version, error) {
	if len(envelope) < envelopeHeaderBytes+envelopeNonceBytes+envelopeTagBytes || len(envelope) > MaxEnvelopeBytes {
		return nil, Version{}, ErrCorrupt
	}
	header := envelope[:envelopeHeaderBytes]
	if string(header[:8]) != string(envelopeMagic[:]) || header[8] != envelopeVersion ||
		header[9] != envelopeAlgorithm || header[10] != envelopeNonceBytes || header[11] != envelopeFlags {
		return nil, Version{}, ErrCorrupt
	}
	ciphertextLen := uint64(binary.BigEndian.Uint32(header[12:16]))
	if ciphertextLen < envelopeTagBytes || ciphertextLen > MaxValueBytes+envelopeTagBytes ||
		uint64(len(envelope)) != envelopeHeaderBytes+envelopeNonceBytes+ciphertextLen {
		return nil, Version{}, ErrCorrupt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, Version{}, ErrCorrupt
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, Version{}, ErrCorrupt
	}
	nonce := envelope[envelopeHeaderBytes : envelopeHeaderBytes+envelopeNonceBytes]
	plaintext, err := aead.Open(nil, nonce, envelope[envelopeHeaderBytes+envelopeNonceBytes:], envelopeAAD(header, namespace, recordKey))
	if err != nil {
		return nil, Version{}, ErrCorrupt
	}
	return plaintext, versionFromEnvelope(envelope), nil
}

func envelopeAAD(header, namespace, recordKey []byte) []byte {
	return frameFields(aadDomain, header, namespace, recordKey)
}

func versionFromEnvelope(envelope []byte) Version {
	return Version{token: sha256.Sum256(envelope), valid: true}
}

func readBounded(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxEnvelopeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read credential envelope: %w", ErrUnavailable)
	}
	if len(data) > MaxEnvelopeBytes {
		return nil, ErrCorrupt
	}
	return data, nil
}
