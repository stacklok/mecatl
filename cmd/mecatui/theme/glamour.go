package theme

import "charm.land/glamour/v2/ansi"

// strptr is a tiny helper: glamour's StylePrimitive carries *string colours so
// "unset" (nil) is distinguishable from "set to default". We always set a
// concrete colour, so wrap the hex.
func strptr(s string) *string { return &s }

func boolptr(b bool) *bool { return &b }

// GlamourStyle maps the theme's semantic slots onto a glamour ansi.StyleConfig
// for markdown rendering. It is built from scratch (the glamour styles package
// in v2 ships only ASCII/Dracula/TokyoNight, no neutral DarkStyleConfig to
// clone), so every colour is driven by the palette — this is what makes the
// markdown obey the active theme. Code-block syntax colours come from the
// syntax* slots via the Chroma config.
func (t Theme) GlamourStyle() ansi.StyleConfig {
	p := t.Palette
	indent := uint(0)
	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: strptr(p.MdText),
			},
			Indent: &indent,
		},
		Paragraph: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: strptr(p.MdText)},
		},
		Text: ansi.StylePrimitive{Color: strptr(p.MdText)},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:  strptr(p.MdQuote),
				Italic: boolptr(true),
			},
			IndentToken: strptr("┃ "),
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: strptr(p.MdHeading),
				Bold:  boolptr(true),
			},
		},
		H1: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix:          " ",
				Suffix:          " ",
				Color:           strptr(p.Bg),
				BackgroundColor: strptr(p.MdHeading),
				Bold:            boolptr(true),
			},
		},
		H2: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "## ",
				Color:  strptr(p.MdHeading),
				Bold:   boolptr(true),
			},
		},
		H3: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "### ",
				Color:  strptr(p.MdHeading),
				Bold:   boolptr(true),
			},
		},
		H4: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Prefix: "#### ", Color: strptr(p.MdHeading), Bold: boolptr(true)},
		},
		H5: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Prefix: "##### ", Color: strptr(p.MdHeading), Bold: boolptr(true)},
		},
		H6: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Prefix: "###### ", Color: strptr(p.MdHeading), Bold: boolptr(true)},
		},
		Strong: ansi.StylePrimitive{Color: strptr(p.Text), Bold: boolptr(true)},
		Emph:   ansi.StylePrimitive{Color: strptr(p.MdText), Italic: boolptr(true)},
		HorizontalRule: ansi.StylePrimitive{
			Color:  strptr(p.BorderSubtle),
			Format: "\n─────────────────────\n",
		},
		Item: ansi.StylePrimitive{Color: strptr(p.MdText)},
		// BlockPrefix is the marker→text separator glamour appends after the
		// rendered number ("1" + ". " → "1. text"); without it an ordered list
		// renders "1First item". Stock glamour styles set this (styles.go); our
		// from-scratch config must too.
		Enumeration: ansi.StylePrimitive{Color: strptr(p.Accent), BlockPrefix: ". "},
		Link: ansi.StylePrimitive{
			Color:     strptr(p.MdLink),
			Underline: boolptr(true),
		},
		LinkText: ansi.StylePrimitive{Color: strptr(p.MdLink), Bold: boolptr(true)},
		Code: ansi.StyleBlock{
			// Inline code is DE-EMPHASISED: a receding foreground (the quote slot,
			// palette-derived so it stays theme-safe) plus Faint, over the element
			// background. It reads as a quiet monospace span rather than an accent,
			// so prose around an `identifier` no longer fights it for attention.
			StylePrimitive: ansi.StylePrimitive{
				Color:           strptr(p.MdQuote),
				BackgroundColor: strptr(p.BgElement),
				Faint:           boolptr(true),
				Prefix:          " ",
				Suffix:          " ",
			},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{Color: strptr(p.MdText)},
				Margin:         &indent,
			},
			Chroma: t.chroma(),
		},
		Table: ansi.StyleTable{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{Color: strptr(p.MdText)},
			},
		},
		List: ansi.StyleList{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{Color: strptr(p.MdText)},
			},
		},
	}
}

// chroma maps the syntax* palette slots onto glamour's code-block chroma. Only
// the slots we expose are coloured; the rest fall back to body text. This is
// what themes the fenced-code syntax highlighting.
func (t Theme) chroma() *ansi.Chroma {
	p := t.Palette
	return &ansi.Chroma{
		Text:                ansi.StylePrimitive{Color: strptr(p.SynVariable)},
		Error:               ansi.StylePrimitive{Color: strptr(p.Error)},
		Comment:             ansi.StylePrimitive{Color: strptr(p.SynComment), Italic: boolptr(true)},
		CommentPreproc:      ansi.StylePrimitive{Color: strptr(p.SynComment)},
		Keyword:             ansi.StylePrimitive{Color: strptr(p.SynKeyword), Bold: boolptr(true)},
		KeywordReserved:     ansi.StylePrimitive{Color: strptr(p.SynKeyword)},
		KeywordNamespace:    ansi.StylePrimitive{Color: strptr(p.SynKeyword)},
		KeywordType:         ansi.StylePrimitive{Color: strptr(p.SynType)},
		Operator:            ansi.StylePrimitive{Color: strptr(p.SynOperator)},
		Punctuation:         ansi.StylePrimitive{Color: strptr(p.SynPunctuation)},
		Name:                ansi.StylePrimitive{Color: strptr(p.SynVariable)},
		NameBuiltin:         ansi.StylePrimitive{Color: strptr(p.SynType)},
		NameTag:             ansi.StylePrimitive{Color: strptr(p.SynKeyword)},
		NameAttribute:       ansi.StylePrimitive{Color: strptr(p.SynFunction)},
		NameClass:           ansi.StylePrimitive{Color: strptr(p.SynType), Bold: boolptr(true)},
		NameConstant:        ansi.StylePrimitive{Color: strptr(p.SynNumber)},
		NameDecorator:       ansi.StylePrimitive{Color: strptr(p.SynFunction)},
		NameFunction:        ansi.StylePrimitive{Color: strptr(p.SynFunction), Bold: boolptr(true)},
		LiteralNumber:       ansi.StylePrimitive{Color: strptr(p.SynNumber)},
		LiteralString:       ansi.StylePrimitive{Color: strptr(p.SynString)},
		LiteralStringEscape: ansi.StylePrimitive{Color: strptr(p.SynKeyword)},
		GenericDeleted:      ansi.StylePrimitive{Color: strptr(p.Error)},
		GenericInserted:     ansi.StylePrimitive{Color: strptr(p.Success)},
		GenericEmph:         ansi.StylePrimitive{Italic: boolptr(true)},
		GenericStrong:       ansi.StylePrimitive{Bold: boolptr(true)},
		Background:          ansi.StylePrimitive{BackgroundColor: strptr(p.BgPanel)},
	}
}
