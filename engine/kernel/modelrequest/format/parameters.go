package format

import (
	"errors"
	"unicode/utf8"
)

// ParametersV1 contains chronological-v1 construction choices, not request data.
// Zero explicitly omits all private envelopes. Nil and empty lists encode alike.
// Assignment shares selectors; Clone detaches them.
//
// Provisional: this exact allow-list is best-effort authorization, not proof of
// adapter replay compatibility. Reassess with actual adapter policy discovery;
// it never permits wildcard matching or reinterpretation of opaque bytes.
//
//dtogen:include
type ParametersV1 struct {
	// PrivateReplay selects whole envelopes by exact source API/model and envelope
	// metadata. Order cannot reorder conversation; duplicate matches do not duplicate
	// envelopes. Nil and empty both select none, never ambient defaults.
	PrivateReplay []PrivateReplaySelector `cbor:"p"`
}

// Validate checks each exact selector's syntax without resource policy.
func (v ParametersV1) Validate() error {
	for _, s := range v.PrivateReplay {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// PrivateReplaySelector matches one originating request target and whole private
// envelope metadata tuple. Zero is invalid; all strings are exact, without wildcards.
// Its compatibility significance is provisional as documented on ParametersV1.
//
//dtogen:validate Validate
type PrivateReplaySelector struct {
	// SourceAPI is the required nonempty UTF-8 originating request API, not current API.
	SourceAPI string `cbor:"a"`
	// SourceModel is the required nonempty UTF-8 originating request model.
	SourceModel string `cbor:"m"`
	// Producer is the required nonempty UTF-8 envelope producer identifier.
	Producer string `cbor:"p"`
	// Format is the required nonempty UTF-8 producer-owned format label.
	Format string `cbor:"f"`
	// Version is the required positive producer-owned format revision.
	Version int64 `cbor:"v"`
}

// Validate checks exact-match metadata syntax, not compatibility or authority.
func (v PrivateReplaySelector) Validate() error {
	for _, s := range []string{v.SourceAPI, v.SourceModel, v.Producer, v.Format} {
		if s == "" || !utf8.ValidString(s) {
			return errors.New("private replay selector requires nonempty UTF-8 labels")
		}
	}
	if v.Version <= 0 {
		return errors.New("private replay selector requires positive version")
	}
	return nil
}
