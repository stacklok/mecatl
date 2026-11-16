package modelrequest

import (
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
)

// Request is an immutable logical request over privately owned DTO backing and its
// canonical content CID. Copies share immutable storage. Zero is unset, with safe
// empty accessors and an unset CID. No public mutable-DTO admission is provided.
//
//dtogen:include
type Request struct {
	value *format.RequestV1
	cid   contentid.CID
}

// CID returns canonical request-byte identity, or unset for a zero Request.
func (v Request) CID() contentid.CID { return v.cid }

// finalizeRequest admits stable trusted in-process DTO input under explicit
// bounds before encoding or cloning. It retains no mutable caller storage.
// Failure returns an unset request; successful bytes alone determine its CID.
func finalizeRequest(v format.RequestV1, limits codec.Limits) (Request, error) {
	encoded, err := codec.Encode(v, limits)
	if err != nil {
		return Request{}, err
	}
	owned := v.Clone()
	return Request{value: &owned, cid: contentid.Sum(encoded)}, nil
}
