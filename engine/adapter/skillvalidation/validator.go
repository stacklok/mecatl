// Package skillvalidation provides logical admission checks for agent-owned skills.
package skillvalidation

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/tool"
)

var (
	absoluteMachinePath = regexp.MustCompile(`(?i)(?:^|[\s("'` + "`" + `])(?:/[^\s)"'` + "`" + `]+|[a-z]:\\[^\s)"'` + "`" + `]+)`)
	toolAuthorityClaim  = regexp.MustCompile(`(?i)\b(?:pre-?approved|permission (?:is |has been )?granted|bypass (?:the )?permission|automatically allowed|no approval (?:is )?needed)\b`)
)

// Validator applies logical, storage-free learned-skill admission checks.
type Validator struct{}

var _ learning.SkillValidator = Validator{}

// Validate applies bounded structure, security, and inventory-collision checks.
//
//nolint:gocyclo // admission intentionally evaluates every bounded security and collision rule
func (Validator) Validate(ctx context.Context, request learning.SkillValidationRequest) (learning.SkillValidation, error) {
	if err := ctx.Err(); err != nil {
		return learning.SkillValidation{}, err
	}
	if err := learning.ValidateSkillPartition(request.Partition, request.OwnerAgent); err != nil {
		return learning.SkillValidation{}, err
	}
	if err := learning.ValidateSkillBundle(request.Bundle); err != nil {
		return learning.SkillValidation{}, err
	}
	if !skillfs.ValidSkillName(request.Bundle.Name) {
		return learning.SkillValidation{}, learning.ErrInvalidSkill
	}
	if err := learning.ValidateSkillProvenance(request.Provenance); err != nil {
		return learning.SkillValidation{}, err
	}
	if len(request.Assets) != 0 {
		return learning.SkillValidation{}, errors.New("skillvalidation: generated assets and scripts are unsupported in v1")
	}
	text := request.Bundle.Description + "\n" + request.Bundle.Body
	if _, found := skillfs.ScanForInjection(text); found || tool.DirectiveShapedUserMemory(text) {
		return learning.SkillValidation{}, errors.New("skillvalidation: instruction framing override")
	}
	if secretShaped(request.Bundle.Name, request.Bundle.Description) || secretShaped(request.Bundle.Name, request.Bundle.Body) {
		return learning.SkillValidation{}, errors.New("skillvalidation: secret-shaped content")
	}
	if absoluteMachinePath.MatchString(text) {
		return learning.SkillValidation{}, errors.New("skillvalidation: absolute machine path")
	}
	if toolAuthorityClaim.MatchString(text) {
		return learning.SkillValidation{}, errors.New("skillvalidation: tool permission claim")
	}

	result := learning.SkillValidation{Disposition: learning.ValidationAccept}
	for _, item := range request.Inventory {
		if item.Name == request.Bundle.Name {
			if !item.AgentOwned || item.OwnerAgent != request.OwnerAgent {
				return learning.SkillValidation{}, learning.ErrSkillNameCollision
			}
			if item.Bundle == request.Bundle {
				duplicate := item
				result.Disposition = learning.ValidationExactDuplicate
				result.Duplicate = &duplicate
				return result, nil
			}
			result.Similar = append(result.Similar, item)
			continue
		}
		if similarName(item.Name, request.Bundle.Name) || similarText(item.Bundle.Body, request.Bundle.Body) {
			result.Similar = append(result.Similar, item)
		}
	}
	if len(result.Similar) > 0 {
		sort.Slice(result.Similar, func(i, j int) bool { return result.Similar[i].Name < result.Similar[j].Name })
		if len(result.Similar) > 8 {
			result.Similar = result.Similar[:8]
		}
		result.Disposition = learning.ValidationSimilarStageHint
	}
	return result, nil
}

func secretShaped(key, text string) bool {
	if tool.SecretShapedMemoryValue(key, text) {
		return true
	}
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;()[]{}<>\"'", r)
	}) {
		if tool.SecretShapedMemoryValue(key, strings.TrimRight(token, ".:!?")) {
			return true
		}
	}
	return false
}

func similarName(a, b string) bool {
	a = strings.ReplaceAll(a, "_", "-")
	b = strings.ReplaceAll(b, "_", "-")
	return strings.TrimSuffix(a, "s") == strings.TrimSuffix(b, "s") || strings.Contains(a, b) || strings.Contains(b, a)
}
func similarText(a, b string) bool {
	words := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if len([]rune(w)) >= 5 {
				out[w] = true
			}
		}
		return out
	}
	x, y := words(a), words(b)
	if len(x) < 4 || len(y) < 4 {
		return false
	}
	common := 0
	for w := range x {
		if y[w] {
			common++
		}
	}
	small := min(len(x), len(y))
	return common*2 >= small
}
