package theme

// Recurring hex values, named once so the goconst linter (and humans) see one
// source of truth per built-in's signature colours.
const (
	aztecGold = "#E9B949" // aztec accent / warning / user / code
	monoBlue  = "#5FAFFF" // mono accent / link / info / type / border-active
	solarBlue = "#268BD2" // solar primary / link-ish / info
	solarGold = "#B58900" // solar accent / user / code / number
)

var aztecTranscript = transcriptRecipe{
	llm: "#c98f9d", tool: "#57b8bf", user: "#e9b949", harness: "#aaa0d4",
	text: "#e7e2d3", harnessText: "#bac8bf", run: "#6b857a",
	reasoning: "#9aaba0", stats: "#9aaba0",
}

var solarTranscript = transcriptRecipe{
	llm: "#a13d62", tool: "#006f7a", user: "#955b00", harness: "#713bb0",
	text: "#202d28", harnessText: "#40554a", run: "#687b70",
	reasoning: "#52665b", stats: "#52665b",
}

// aztecPalette is the default mecatui palette: jade/turquoise + gold on obsidian,
// terracotta for errors, bone-coloured text. Tuned for dark terminals. It is
// also the merge base for partial user themes (see load.go), so every slot here
// must be populated.
var aztecPalette = Palette{
	Primary:   "#1FB39A",
	Secondary: "#2A9D8F",
	Accent:    aztecGold,

	Bg:        "#0E1311",
	BgPanel:   "#15201C",
	BgElement: "#1C2A24",

	Border:       "#2E4039",
	BorderActive: "#1FB39A",
	BorderSubtle: "#1E2A25",

	Text:      "#E7E2D3",
	TextMuted: "#8A998F",

	Selection: "#2A4D45", // dark jade block → near-white fg

	Success: "#4CC38A",
	Warning: aztecGold,
	Error:   "#D9603B",
	Info:    "#3AAFB9",

	User:      aztecGold,
	Assistant: "#E7E2D3",
	Tool:      "#3AAFB9",

	MdText:    "#E7E2D3",
	MdHeading: "#1FB39A",
	MdLink:    "#3AAFB9",
	MdCode:    aztecGold,
	MdQuote:   "#8A998F",

	SynComment:     "#5E6B63",
	SynKeyword:     "#D9603B",
	SynFunction:    "#E9B949",
	SynVariable:    "#E7E2D3",
	SynString:      "#4CC38A",
	SynNumber:      "#E08E45",
	SynType:        "#1FB39A",
	SynOperator:    "#3AAFB9",
	SynPunctuation: "#8A998F",
}

// monoPalette is a neutral greyscale-with-blue-accent theme. It exists to prove
// the abstraction is theme-agnostic: dropping a wholly different palette in
// restyles everything with no widget-level changes.
var monoPalette = Palette{
	Primary:   "#C9C9C9",
	Secondary: "#9E9E9E",
	Accent:    monoBlue,

	Bg:        "#0B0B0B",
	BgPanel:   "#151515",
	BgElement: "#1F1F1F",

	Border:       "#3A3A3A",
	BorderActive: monoBlue,
	BorderSubtle: "#262626",

	Text:      "#E4E4E4",
	TextMuted: "#7A7A7A",

	Selection: "#2F4A66", // dark blue block → near-white fg

	Success: "#9ECE6A",
	Warning: "#E0AF68",
	Error:   "#F7768E",
	Info:    monoBlue,

	User:      monoBlue,
	Assistant: "#E4E4E4",
	Tool:      "#9E9E9E",

	MdText:    "#E4E4E4",
	MdHeading: "#C9C9C9",
	MdLink:    monoBlue,
	MdCode:    "#E0AF68",
	MdQuote:   "#7A7A7A",

	SynComment:     "#5A5A5A",
	SynKeyword:     monoBlue,
	SynFunction:    "#C9C9C9",
	SynVariable:    "#E4E4E4",
	SynString:      "#9ECE6A",
	SynNumber:      "#E0AF68",
	SynType:        monoBlue,
	SynOperator:    "#9E9E9E",
	SynPunctuation: "#7A7A7A",
}

// solarPalette is a warm light-leaning variant (solarized-light-ish) — a third
// built-in to demonstrate light-terminal tuning. v2 dropped AdaptiveColor, so
// light/dark is just a different explicit palette.
var solarPalette = Palette{
	Primary:   solarBlue,
	Secondary: "#2AA198",
	Accent:    solarGold,

	Bg:        "#FDF6E3",
	BgPanel:   "#EEE8D5",
	BgElement: "#E4DCC4",

	Border:       "#93A1A1",
	BorderActive: solarBlue,
	BorderSubtle: "#DDD6C1",

	Text:      "#073642",
	TextMuted: "#657B83",

	Selection: "#CFC8B0", // light sand block → near-black fg (exercises the flip)

	Success: "#859900",
	Warning: solarGold,
	Error:   "#DC322F",
	Info:    solarBlue,

	User:      solarGold,
	Assistant: "#073642",
	Tool:      "#2AA198",

	MdText:    "#073642",
	MdHeading: solarBlue,
	MdLink:    "#2AA198",
	MdCode:    solarGold,
	MdQuote:   "#657B83",

	SynComment:     "#93A1A1",
	SynKeyword:     "#859900",
	SynFunction:    solarBlue,
	SynVariable:    "#073642",
	SynString:      "#2AA198",
	SynNumber:      "#D33682",
	SynType:        solarGold,
	SynOperator:    "#DC322F",
	SynPunctuation: "#657B83",
}

// AztecPalette returns a copy of the built-in Aztec palette. Exposed so load.go
// (and tests) can use it as the merge base for partial user themes.
func AztecPalette() Palette { return aztecPalette }
