package port

// ProviderErrorMetadataError exposes optional provider failure metadata through
// errors.As using only primitive method signatures. That structural shape lets
// independently versioned provider modules implement it while compiling against
// an older engine release.
//
// Zero values mean absent. Consumers must validate all populated fields and omit
// invalid metadata as a whole. CorrelationKind is a closed root-validated string
// vocabulary: request, response, trace, completion, or message.
type ProviderErrorMetadataError interface {
	error
	ProviderHTTPStatus() int
	ProviderInBandStatus() int
	ProviderErrorCode() string
	ProviderErrorCorrelationKind() string
	ProviderErrorCorrelationID() string
}
