//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"io"
)

const (
	plainDirectory       = "clientauth-plaintext"
	plainHeaderBytes     = 16
	plainGenerationBytes = 16
	plainVersion         = byte(1)
	plainRecordDomain    = "mecatl/credentialstore/plain-record/v1"
)

var plainMagic = [8]byte{'M', 'E', 'C', 'A', 'T', 'L', 'P', 0}

func sealPlainRecord(namespace, recordKey, value []byte, random io.Reader) ([]byte, Version, error) {
	if random == nil {
		random = rand.Reader
	}
	out := make([]byte, plainHeaderBytes+plainGenerationBytes+len(value))
	copy(out[:8], plainMagic[:])
	out[8] = plainVersion
	binary.BigEndian.PutUint32(out[12:16], uint32(len(value))) // #nosec G115 -- value is bounded to one MiB.
	if _, err := io.ReadFull(random, out[plainHeaderBytes:plainHeaderBytes+plainGenerationBytes]); err != nil {
		return nil, Version{}, unavailable("generate plaintext credential record", err)
	}
	copy(out[plainHeaderBytes+plainGenerationBytes:], value)
	return out, plainRecordVersion(namespace, recordKey, out), nil
}

func openPlainRecord(namespace, recordKey, record []byte) ([]byte, Version, error) {
	if len(record) < plainHeaderBytes+plainGenerationBytes || len(record) > plainHeaderBytes+plainGenerationBytes+MaxValueBytes ||
		!bytes.Equal(record[:8], plainMagic[:]) || record[8] != plainVersion || record[9] != 0 || record[10] != 0 || record[11] != 0 {
		return nil, Version{}, ErrCorrupt
	}
	valueLen := binary.BigEndian.Uint32(record[12:16])
	if uint64(len(record)) != uint64(plainHeaderBytes+plainGenerationBytes)+uint64(valueLen) {
		return nil, Version{}, ErrCorrupt
	}
	value := bytes.Clone(record[plainHeaderBytes+plainGenerationBytes:])
	return value, plainRecordVersion(namespace, recordKey, record), nil
}

func plainRecordVersion(namespace, recordKey, record []byte) Version {
	return Version{token: sha256.Sum256(frameFields(plainRecordDomain, namespace, recordKey, record)), valid: true}
}
