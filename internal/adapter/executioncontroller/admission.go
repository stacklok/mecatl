package executioncontroller

import (
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/stacklok/mecatl/internal/executionenv"
)

const admittedCapabilitiesField = "admittedCapabilities"

// admissionRefusal is a capability refusal whose message is safe to return
// to the caller.
type admissionRefusal struct{ cause *executionenv.Error }

func (e *admissionRefusal) Error() string { return e.cause.Error() }
func (e *admissionRefusal) Unwrap() error { return e.cause }

func refuseAdmission(code executionenv.ErrorCode, format string, args ...any) error {
	return &admissionRefusal{&executionenv.Error{Code: code, Message: fmt.Sprintf(format, args...)}}
}

// admitCapabilities returns the canonical admitted set for a request against a
// template's declared set; an empty request asks for the declared set.
func admitCapabilities(requested, declared []string) ([]string, error) {
	declared = canonicalCapabilities(declared)
	if len(requested) == 0 {
		requested = declared
	}
	for i, name := range requested {
		known := name == CapabilityFilesystem || name == CapabilityForegroundCommand
		if !known || slices.Contains(requested[:i], name) {
			return nil, refuseAdmission(
				executionenv.CodeInvalidArgument,
				"execution capability request must list %q or %q at most once each",
				CapabilityFilesystem,
				CapabilityForegroundCommand,
			)
		}
		if !slices.Contains(declared, name) {
			return nil, refuseAdmission(
				executionenv.CodePermissionDenied,
				"execution template does not declare capability %q",
				name,
			)
		}
	}
	if len(requested) != 2 {
		return nil, refuseAdmission(
			executionenv.CodeInvalidArgument,
			"sessions must have both %q and %q; file-only sessions are not supported yet",
			CapabilityFilesystem,
			CapabilityForegroundCommand,
		)
	}
	return slices.Sorted(slices.Values(requested)), nil
}

// admittedCapabilities reads the environment's recorded set. Environments
// created before admission was recorded report their template's declared set;
// every such template declared both capabilities.
func (p *Profiles) admittedCapabilities(o *unstructured.Unstructured) ([]string, error) {
	recorded, found, err := unstructured.NestedStringSlice(
		o.Object,
		"spec",
		admittedCapabilitiesField,
	)
	if err != nil || (found && !executionenv.ValidAdmittedCapabilities(recorded)) {
		return nil, &executionenv.Error{
			Code:    executionenv.CodeConflict,
			Message: "admitted capabilities are invalid",
		}
	}
	if found {
		return slices.Sorted(slices.Values(recorded)), nil
	}
	if profile, ok := p.forEnvironment(o); ok {
		return canonicalCapabilities(profile.Spec.Capabilities), nil
	}
	return executionenv.TemplateCapabilities(), nil
}

func toAnySlice(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

func (p *Profiles) admittedCapabilitiesMatch(
	o *unstructured.Unstructured,
	admitted []string,
) error {
	recorded, err := p.admittedCapabilities(o)
	if err != nil {
		return err
	}
	if !slices.Equal(recorded, admitted) {
		return refuseAdmission(
			executionenv.CodeConflict,
			"binding was already admitted with different execution capabilities",
		)
	}
	return nil
}
