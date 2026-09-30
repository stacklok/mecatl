package boatenv

import (
	"fmt"
	"regexp/syntax"
	"strings"
	"unicode"
)

// maxTranslatedPattern bounds the Python pattern a Grep sends to the guest.
// Expanded Unicode classes can be long, but not unboundedly so.
const maxTranslatedPattern = 256 << 10

// translateRE2 turns an RE2 pattern (the syntax the Grep tool advertises)
// into a Python re pattern that accepts exactly the same lines. The pattern
// is parsed by Go's own regexp/syntax, and every class and case fold is
// emitted as explicit code-point ranges, so nothing depends on how Python
// interprets \w, \d, [[:space:]], \p{...}, or (?i). The caller compiles it
// with re.ASCII so \b and \B keep RE2's ASCII word boundary.
func translateRE2(pattern string) (string, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := emitRE(&b, re.Simplify()); err != nil {
		return "", err
	}
	if b.Len() > maxTranslatedPattern {
		return "", fmt.Errorf("pattern expands beyond %d bytes", maxTranslatedPattern)
	}
	return b.String(), nil
}

// fixedOps are the constructs with a fixed Python spelling.
var fixedOps = map[syntax.Op]string{
	syntax.OpNoMatch:        "(?!)",
	syntax.OpEmptyMatch:     "(?:)",
	syntax.OpAnyCharNotNL:   `[^\n]`,
	syntax.OpAnyChar:        `(?s:.)`,
	syntax.OpBeginLine:      `(?m:^)`,
	syntax.OpEndLine:        `(?m:$)`,
	syntax.OpBeginText:      `\A`,
	syntax.OpEndText:        `\Z`,
	syntax.OpWordBoundary:   `\b`,
	syntax.OpNoWordBoundary: `\B`,
}

func emitRE(b *strings.Builder, re *syntax.Regexp) error {
	if fixed, ok := fixedOps[re.Op]; ok {
		b.WriteString(fixed)
		return nil
	}
	switch re.Op {
	case syntax.OpLiteral:
		emitLiteral(b, re)
	case syntax.OpCharClass:
		emitClass(b, re.Rune)
	case syntax.OpCapture:
		return emitSequence(b, re.Sub, "")
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		return emitRepeat(b, re)
	case syntax.OpConcat:
		return emitSequence(b, re.Sub, "")
	case syntax.OpAlternate:
		return emitSequence(b, re.Sub, "|")
	default:
		return fmt.Errorf("unsupported regexp construct %v", re.Op)
	}
	return nil
}

func emitLiteral(b *strings.Builder, re *syntax.Regexp) {
	b.WriteString("(?:")
	for _, r := range re.Rune {
		if re.Flags&syntax.FoldCase != 0 {
			emitClass(b, foldRanges(r))
		} else {
			emitRune(b, r)
		}
	}
	b.WriteString(")")
}

// emitSequence writes subs as one non-capturing group, joined by sep.
func emitSequence(b *strings.Builder, subs []*syntax.Regexp, sep string) error {
	b.WriteString("(?:")
	for i, sub := range subs {
		if i > 0 {
			b.WriteString(sep)
		}
		if err := emitRE(b, sub); err != nil {
			return err
		}
	}
	b.WriteString(")")
	return nil
}

func emitRepeat(b *strings.Builder, re *syntax.Regexp) error {
	if err := emitSequence(b, re.Sub[:1], ""); err != nil {
		return err
	}
	switch re.Op {
	case syntax.OpStar:
		b.WriteString("*")
	case syntax.OpPlus:
		b.WriteString("+")
	case syntax.OpQuest:
		b.WriteString("?")
	default:
		if re.Max < 0 {
			fmt.Fprintf(b, "{%d,}", re.Min)
		} else {
			fmt.Fprintf(b, "{%d,%d}", re.Min, re.Max)
		}
	}
	if re.Flags&syntax.NonGreedy != 0 {
		b.WriteString("?")
	}
	return nil
}

// foldRanges is the set of runes RE2 treats as equal to r under (?i):
// its simple case-folding orbit.
func foldRanges(r rune) []rune {
	var out []rune
	for f := r; ; {
		out = append(out, f, f)
		f = unicode.SimpleFold(f)
		if f == r {
			break
		}
	}
	return out
}

// emitClass writes a class from lo/hi rune pairs. An empty class matches
// nothing.
func emitClass(b *strings.Builder, ranges []rune) {
	if len(ranges) == 0 {
		b.WriteString("(?!)")
		return
	}
	b.WriteString("[")
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		emitRune(b, lo)
		if hi != lo {
			b.WriteString("-")
			emitRune(b, hi)
		}
	}
	b.WriteString("]")
}

// emitRune writes r as an escape Python re understands inside and outside a
// class, so no rune can change the pattern's structure.
func emitRune(b *strings.Builder, r rune) {
	switch {
	case r < 0x100:
		fmt.Fprintf(b, `\x%02x`, r)
	case r < 0x10000:
		fmt.Fprintf(b, `\u%04x`, r)
	default:
		fmt.Fprintf(b, `\U%08x`, r)
	}
}
