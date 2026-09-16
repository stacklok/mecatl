package main

import (
	"io"
	"strings"
	"unicode"

	"github.com/stacklok/mecatl/cmd/mecatui/statusline"
)

const terminalTitleRunes = 160

// terminalTitleController owns title emission through Bubble Tea's renderer
// output writer. Set runs during View; Write is the renderer-serialized path.
type terminalTitleController struct {
	output   io.Writer
	enabled  bool
	renderer *statusline.TitleRenderer
	pending  string
	last     string
	wrote    bool
	debug    bool
}

func newTerminalTitleController(output io.Writer, enabled bool, renderer *statusline.TitleRenderer) *terminalTitleController {
	return &terminalTitleController{output: output, enabled: enabled, renderer: renderer}
}

func terminalTitleEnabled(cfg config, settings terminalTitleSettings) bool {
	if cfg.terminalTitleFlagSet || cfg.terminalTitleOff {
		return !cfg.terminalTitleOff
	}
	return settings.Enabled
}

func (c *terminalTitleController) Set(input statusline.Input) {
	if !c.enabled {
		return
	}
	title, err := c.renderer.Render(input)
	if err != nil {
		return
	}
	if c.debug {
		title = "DEBUG " + title
	}
	c.pending = sanitizeTerminalTitle(title)
}

func (c *terminalTitleController) Write(p []byte) (int, error) {
	if c.enabled && c.pending != c.last && (c.pending != "" || c.wrote) {
		if _, err := io.WriteString(c.output, "\x1b]0;"+c.pending+"\a"); err != nil {
			return 0, err
		}
		c.last = c.pending
		if c.last != "" {
			c.wrote = true
		}
	}
	return c.output.Write(p)
}

func (c *terminalTitleController) Close() error {
	if c.enabled && c.wrote && c.last != "" {
		if _, err := io.WriteString(c.output, "\x1b]0;\a"); err != nil {
			return err
		}
		c.last = ""
	}
	return nil
}

func sanitizeTerminalTitle(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	space := true
	count := 0
	for _, r := range value {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if space && out.Len() > 0 {
			if count == terminalTitleRunes {
				break
			}
			out.WriteByte(' ')
			count++
		}
		space = false
		if count == terminalTitleRunes {
			break
		}
		out.WriteRune(r)
		count++
	}
	return out.String()
}
