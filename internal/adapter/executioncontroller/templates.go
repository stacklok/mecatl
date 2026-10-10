package executioncontroller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/goccy/go-yaml"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

// TemplatesFile is the single operator-owned execution definition source. A
// published revision must remain in this file while any allocation retains it.
type TemplatesFile struct {
	Templates map[string]TemplateDefinition `yaml:"templates"`
}

// TemplateDefinition is the operator-owned template and revision declaration.
type TemplateDefinition struct {
	Default   string                      `yaml:"default"`
	Revisions map[string]TemplateRevision `yaml:"revisions"`
}

// TemplateRevision is one immutable execution definition and its lifecycle policy.
type TemplateRevision struct {
	Execution  ProfileSpec     `yaml:"execution"`
	Deprecated bool            `yaml:"deprecated,omitempty"`
	Revoked    bool            `yaml:"revoked,omitempty"`
	Display    TemplateDisplay `yaml:"display,omitempty"`
}

// TemplateDisplay is inert operator-authored data, never injected into prompts.
// Extensions are bounded, versioned, namespaced string maps for client display.
type TemplateDisplay struct {
	Name        string                       `yaml:"name,omitempty" json:"name,omitempty"`
	Description string                       `yaml:"description,omitempty" json:"description,omitempty"`
	Extensions  map[string]map[string]string `yaml:"extensions,omitempty" json:"extensions,omitempty"`
}

// TemplatePolicy describes whether a revision remains selectable or retained only.
type TemplatePolicy struct {
	Deprecated bool
	Revoked    bool
}

// LoadTemplates rejects unknown fields and any revision not matching its v1
// canonical execution digest. Display and policy edits do not change revisions.
func LoadTemplates(path string) (*Profiles, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read execution templates: %w", err)
	}
	var f TemplatesFile
	if err := yaml.UnmarshalWithOptions(b, &f, yaml.DisallowUnknownField()); err != nil {
		return nil, fmt.Errorf("decode execution templates: %w", err)
	}
	if len(f.Templates) == 0 || len(f.Templates) > 64 {
		return nil, errors.New("execution templates require 1..64 IDs")
	}
	p := &Profiles{byName: make(map[string]resolvedProfile), defaultRevision: make(map[string]string), revisions: make(map[string]map[string]resolvedProfile), eligibility: make(map[string]map[string]TemplatePolicy), catalog: make(map[string]map[string]TemplateDisplay)}
	total := 0
	for id, t := range f.Templates {
		total += len(t.Revisions)
		if total > 64 {
			return nil, errors.New("execution template inventory exceeds 64 revisions")
		}
		if len(validation.IsDNS1123Label(id)) != 0 || len(t.Revisions) == 0 || len(t.Revisions) > 32 {
			return nil, fmt.Errorf("invalid execution template %q", id)
		}
		p.revisions[id] = make(map[string]resolvedProfile)
		p.eligibility[id] = make(map[string]TemplatePolicy)
		p.catalog[id] = make(map[string]TemplateDisplay)
		for revision, definition := range t.Revisions {
			resolved, err := validateProfile(id, definition.Execution)
			if err != nil {
				return nil, err
			}
			canonical, err := json.Marshal(definition.Execution)
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(append([]byte("mecatl/execution-template/v1\x00"), canonical...))
			want := "v1-" + hex.EncodeToString(sum[:])
			if revision != want {
				return nil, fmt.Errorf("execution template %q revision does not match canonical execution definition: expected %s", id, want)
			}
			if err := validateTemplateDisplay(definition.Display); err != nil {
				return nil, fmt.Errorf("execution template %q display: %w", id, err)
			}
			resolved.Spec, resolved.Digest = definition.Execution, "sha256:"+hex.EncodeToString(sum[:])
			p.revisions[id][revision] = resolved
			p.eligibility[id][revision] = TemplatePolicy{Deprecated: definition.Deprecated, Revoked: definition.Revoked}
			p.catalog[id][revision] = definition.Display
		}
		if t.Default != "" {
			selected, ok := p.revisions[id][t.Default]
			if !ok || p.eligibility[id][t.Default] != (TemplatePolicy{}) {
				return nil, fmt.Errorf("execution template %q requires an eligible default revision", id)
			}
			p.byName[id] = selected
			p.defaultRevision[id] = t.Default
		} else {
			for _, policy := range p.eligibility[id] {
				if policy == (TemplatePolicy{}) {
					return nil, fmt.Errorf("execution template %q has selectable revisions but no default", id)
				}
			}
		}
	}
	return p, nil
}

func validateTemplateDisplay(d TemplateDisplay) error {
	if len(d.Name) > 120 || len(d.Description) > 1024 || len(d.Extensions) > 8 {
		return errors.New("display metadata exceeds bounds")
	}
	for namespace, fields := range d.Extensions {
		if len(namespace) > 238 || !strings.Contains(namespace, "/") || len(fields) == 0 || len(fields) > 16 {
			return errors.New("invalid metadata namespace")
		}
		parts := strings.SplitN(namespace, "/", 2)
		if len(validation.IsDNS1123Subdomain(parts[0])) != 0 || len(validation.IsQualifiedName(namespace)) != 0 {
			return errors.New("invalid metadata namespace")
		}
		if fields["schema_version"] != "1" {
			return errors.New("metadata namespace requires schema_version 1")
		}
		for key, value := range fields {
			if len(validation.IsQualifiedName(key)) != 0 || len(namespace)+1+len(key) > 253 || len(value) > 256 {
				return errors.New("invalid metadata field")
			}
		}
	}
	return nil
}

// DefaultRevision returns the exact operator-pinned revision for an ID.
func (p *Profiles) DefaultRevision(id string) string { return p.defaultRevision[id] }

func (p *Profiles) getRevision(id, revision string) (resolvedProfile, bool) {
	if p == nil {
		return resolvedProfile{}, false
	}
	v, ok := p.revisions[id][revision]
	return v, ok
}

func (p *Profiles) selectRevision(id, revision string) (resolvedProfile, bool) {
	v, ok := p.getRevision(id, revision)
	return v, ok && p.eligibility[id][revision] == (TemplatePolicy{})
}

func (p *Profiles) retainedRevision(id, revision string) (resolvedProfile, bool) {
	v, ok := p.getRevision(id, revision)
	return v, ok && !p.eligibility[id][revision].Revoked
}

func (p *Profiles) forEnvironment(env *unstructured.Unstructured) (resolvedProfile, bool) {
	id := textNested(env.Object, "spec", "templateID")
	revision := textNested(env.Object, "spec", "templateRevision")
	v, ok := p.retainedRevision(id, revision)
	return v, ok && v.Digest == textNested(env.Object, "spec", "templateDigest")
}

func (p *Profiles) isRevoked(env *unstructured.Unstructured) bool {
	_, ok := p.forEnvironment(env)
	return !ok
}

func (p *Profiles) capacity(id string) int {
	limit := 10000
	for _, v := range p.revisions[id] {
		if v.Spec.MaxEnvironments < limit {
			limit = v.Spec.MaxEnvironments
		}
	}
	return limit
}
