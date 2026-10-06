package session

// BrokerSessionRef is an opaque reference to a broker-owned stable session.
// Possession alone does not confer authority.
type BrokerSessionRef string

// BrokerCatalogueRef identifies one immutable broker tool-authority snapshot.
type BrokerCatalogueRef string
