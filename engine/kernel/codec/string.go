package codec

import "github.com/jcalabro/atmos/cbor"

// AppendString appends a text value already admitted by Budget.CheckString.
// The caller must keep the checked value stable; no validation or accounting is repeated.
func AppendString(dst []byte, value string) []byte { return cbor.AppendText(dst, value) }
