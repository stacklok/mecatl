package identityissuer

import (
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// MaxBundleBytes bounds one complete canonical public bundle.
const MaxBundleBytes = 64 << 10

type jwtBundle struct {
	Sequence    uint64      `json:"sequence"`
	RefreshHint int64       `json:"refresh_hint"`
	Keys        []publicJWK `json:"keys"`
}

type publicJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	KID string `json:"kid"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// Bundle returns the canonical public SPIFFE JWT bundle for this immutable key
// generation. refreshHint is advisory; verifiers enforce their own cache bound.
func (i *Issuer) Bundle(refreshHint time.Duration) ([]byte, error) {
	if refreshHint <= 0 || refreshHint > maxBundleCache || refreshHint%time.Second != 0 {
		return nil, errors.New("identity issuer bundle refresh hint is invalid")
	}
	coordinateSize := (elliptic.P256().Params().BitSize + 7) / 8
	key := publicJWK{
		KTY: "EC",
		CRV: "P-256",
		KID: i.kid,
		X:   base64.RawURLEncoding.EncodeToString(i.key.X.FillBytes(make([]byte, coordinateSize))),
		Y:   base64.RawURLEncoding.EncodeToString(i.key.Y.FillBytes(make([]byte, coordinateSize))),
	}
	i.bundleMu.Lock()
	defer i.bundleMu.Unlock()
	i.bundleSequence++
	return json.Marshal(jwtBundle{Sequence: i.bundleSequence, RefreshHint: int64(refreshHint / time.Second), Keys: []publicJWK{key}})
}
