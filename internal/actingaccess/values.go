// Package actingaccess defines the closed, adapter-neutral acting-as-user exchange boundary.
package actingaccess

import (
	"errors"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"
)

const (
	maxValueBytes      = 256
	maxCredentialBytes = 1 << 20
	maxScopes          = 64
)

// Owner is the durable issuer-qualified user identity.
type Owner struct {
	issuer  string
	subject string
}

// NewOwner validates one canonical issuer-qualified identity.
func NewOwner(issuer, subject string) (Owner, error) {
	if !boundedSafe(subject) || !canonicalIssuer(issuer) {
		return Owner{}, errors.New("acting-access owner is invalid")
	}
	return Owner{issuer: issuer, subject: subject}, nil
}

// Issuer returns the canonical issuer identifier.
func (o Owner) Issuer() string { return o.issuer }

// Subject returns the issuer-local subject identifier.
func (o Owner) Subject() string { return o.subject }

// Presenter is the authenticated broker client identity.
type Presenter struct{ value string }

// NewPresenter validates one canonical presenter identifier.
func NewPresenter(value string) (Presenter, error) {
	if !canonicalToken(value) {
		return Presenter{}, errors.New("acting-access presenter is invalid")
	}
	return Presenter{value: value}, nil
}

// Value returns the canonical presenter identifier.
func (p Presenter) Value() string { return p.value }

// RegisteredResource is a registry-selected target resource, never a caller URL.
type RegisteredResource struct{ value string }

// Value returns the canonical registered resource identifier.
func (r RegisteredResource) Value() string { return r.value }

// RegisteredOperation is a registry-selected operation.
type RegisteredOperation struct{ value string }

// Value returns the canonical registered operation identifier.
func (o RegisteredOperation) Value() string { return o.value }

// RegisteredDetail is one exact registry-selected authorization detail.
type RegisteredDetail struct{ value string }

// Value returns the canonical authorization detail.
func (d RegisteredDetail) Value() string { return d.value }

// Scope is one canonical registry-selected scope.
type Scope struct{ value string }

// Value returns the canonical scope identifier.
func (s Scope) Value() string { return s.value }

// Registration is trusted composition input for one exact target tuple.
type Registration struct {
	Resource      string
	Operation     string
	Detail        string
	Scopes        []string
	RequiredTools []string
}

type registeredTarget struct {
	resource      RegisteredResource
	operation     RegisteredOperation
	detail        RegisteredDetail
	scopes        []Scope
	requiredTools []string
}

// Registry contains the complete trusted set of request tuples.
type Registry struct{ targets []registeredTarget }

// NewRegistry validates and copies trusted target registrations.
func NewRegistry(registrations []Registration) (*Registry, error) {
	if len(registrations) == 0 || len(registrations) > maxScopes {
		return nil, errors.New("acting-access registry is invalid")
	}
	registry := &Registry{targets: make([]registeredTarget, 0, len(registrations))}
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if !canonicalToken(registration.Resource) || !canonicalToken(registration.Operation) || !canonicalToken(registration.Detail) ||
			!canonicalStrings(registration.Scopes, canonicalToken) || !canonicalStrings(registration.RequiredTools, boundedSafe) {
			return nil, errors.New("acting-access registration is invalid")
		}
		key := registration.Resource + "\x00" + registration.Operation + "\x00" + registration.Detail + "\x00" + strings.Join(registration.Scopes, "\x00")
		if _, exists := seen[key]; exists {
			return nil, errors.New("acting-access registration is duplicated")
		}
		seen[key] = struct{}{}
		target := registeredTarget{
			resource: RegisteredResource{value: registration.Resource}, operation: RegisteredOperation{value: registration.Operation},
			detail: RegisteredDetail{value: registration.Detail}, scopes: make([]Scope, len(registration.Scopes)), requiredTools: append([]string(nil), registration.RequiredTools...),
		}
		for i, scope := range registration.Scopes {
			target.scopes[i] = Scope{value: scope}
		}
		registry.targets = append(registry.targets, target)
	}
	return registry, nil
}

// Request is the immutable, non-secret input admitted by the registry.
type Request struct {
	owner     Owner
	presenter Presenter
	resource  RegisteredResource
	operation RegisteredOperation
	detail    RegisteredDetail
	scopes    []Scope
}

// NewRequest resolves only an exact registered tuple and copies its values.
func (r *Registry) NewRequest(owner Owner, presenter Presenter, resource, operation, detail string, scopes []string) (Request, error) {
	if r == nil || owner == (Owner{}) || presenter == (Presenter{}) || !canonicalStrings(scopes, canonicalToken) {
		return Request{}, errors.New("acting-access request is invalid")
	}
	for _, target := range r.targets {
		if target.resource.value != resource || target.operation.value != operation || target.detail.value != detail ||
			!slices.EqualFunc(target.scopes, scopes, func(scope Scope, value string) bool { return scope.value == value }) {
			continue
		}
		return Request{owner: owner, presenter: presenter, resource: target.resource, operation: target.operation, detail: target.detail,
			scopes: append([]Scope(nil), target.scopes...)}, nil
	}
	return Request{}, errors.New("acting-access request is not registered")
}

// Owner returns the durable request owner.
func (r Request) Owner() Owner { return r.owner }

// Presenter returns the authenticated presenter.
func (r Request) Presenter() Presenter { return r.presenter }

// Resource returns the registered resource.
func (r Request) Resource() RegisteredResource { return r.resource }

// Operation returns the registered operation.
func (r Request) Operation() RegisteredOperation { return r.operation }

// Detail returns the exact registered authorization detail.
func (r Request) Detail() RegisteredDetail { return r.detail }

// Scopes returns a copy of the canonical scopes.
func (r Request) Scopes() []string {
	out := make([]string, len(r.scopes))
	for i, scope := range r.scopes {
		out[i] = scope.value
	}
	return out
}

func canonicalIssuer(raw string) bool {
	if !boundedSafe(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	cleanPath := parsed.EscapedPath() == "" || path.Clean(parsed.EscapedPath()) == parsed.EscapedPath()
	return parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		parsed.String() == raw && cleanPath
}

func boundedSafe(value string) bool {
	return value != "" && len(value) <= maxValueBytes && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) }) < 0
}

func canonicalToken(value string) bool {
	if !boundedSafe(value) || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && !strings.ContainsRune("._:/-", rune(c)) {
			return false
		}
	}
	return true
}

func canonicalStrings(values []string, valid func(string) bool) bool {
	if len(values) == 0 || len(values) > maxScopes || !sort.StringsAreSorted(values) {
		return false
	}
	for i, value := range values {
		if !valid(value) || (i > 0 && values[i-1] == value) {
			return false
		}
	}
	return true
}

type secretValue struct{ raw string }

// The function marker makes accidental encoding/json serialization fail instead of
// silently producing an apparently safe representation. The indirection prevents fmt
// from traversing and printing the credential bytes.
type subjectSecret struct {
	value *secretValue
	seal  func()
}
type actorSecret struct {
	value *secretValue
	seal  func()
}
type outputSecret struct {
	value *secretValue
	seal  func()
}

// SubjectAssertion is an ephemeral exchange-subject credential.
type SubjectAssertion struct{ secret subjectSecret }

// I2Token is an ephemeral compact logical-agent JWT-SVID.
type I2Token struct{ secret actorSecret }

// OutputToken is an ephemeral issued acting-access token.
type OutputToken struct{ secret outputSecret }

// SubjectAssertionConsumer is the explicit trusted boundary for transient subject bytes.
type SubjectAssertionConsumer interface {
	ConsumeSubjectAssertion([]byte) error
}

// I2TokenConsumer is the explicit trusted boundary for transient actor-token bytes.
type I2TokenConsumer interface {
	ConsumeI2Token([]byte) error
}

// OutputTokenConsumer is the explicit trusted boundary for transient output-token bytes.
type OutputTokenConsumer interface {
	ConsumeOutputToken([]byte) error
}

// Consume passes a temporary copy of the assertion to a trusted verifier and clears it on return.
func (s SubjectAssertion) Consume(consumer SubjectAssertionConsumer) error {
	if s.secret.value == nil || consumer == nil {
		return errors.New("subject assertion is unavailable")
	}
	return consumeSecret(s.secret.value.raw, consumer.ConsumeSubjectAssertion)
}

// Consume passes a temporary copy of the I2 token to a trusted verifier and clears it on return.
func (t I2Token) Consume(consumer I2TokenConsumer) error {
	if t.secret.value == nil || consumer == nil {
		return errors.New("I2 token is unavailable")
	}
	return consumeSecret(t.secret.value.raw, consumer.ConsumeI2Token)
}

// Consume passes a temporary copy of the output token to a trusted verifier and clears it on return.
func (t OutputToken) Consume(consumer OutputTokenConsumer) error {
	if t.secret.value == nil || consumer == nil {
		return errors.New("output token is unavailable")
	}
	return consumeSecret(t.secret.value.raw, consumer.ConsumeOutputToken)
}

func consumeSecret(raw string, consume func([]byte) error) error {
	value := []byte(raw)
	defer clear(value)
	return consume(value)
}

// NewSubjectAssertion wraps subject assertion bytes without formatting or serialization support.
func NewSubjectAssertion(raw string) (SubjectAssertion, error) {
	if raw == "" || len(raw) > maxCredentialBytes {
		return SubjectAssertion{}, errors.New("subject assertion size is invalid")
	}
	return SubjectAssertion{secret: subjectSecret{value: &secretValue{raw: raw}, seal: func() {}}}, nil
}

// NewI2Token wraps compact I2 bytes without formatting or serialization support.
func NewI2Token(raw string) (I2Token, error) {
	if raw == "" || len(raw) > maxCredentialBytes {
		return I2Token{}, errors.New("I2 token size is invalid")
	}
	return I2Token{secret: actorSecret{value: &secretValue{raw: raw}, seal: func() {}}}, nil
}

// NewOutputToken wraps output token bytes without formatting or serialization support.
func NewOutputToken(raw string) (OutputToken, error) {
	if raw == "" || len(raw) > maxCredentialBytes {
		return OutputToken{}, errors.New("output token size is invalid")
	}
	return OutputToken{secret: outputSecret{value: &secretValue{raw: raw}, seal: func() {}}}, nil
}
