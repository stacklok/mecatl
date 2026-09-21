package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/stacklok/mecatl/cmd/mecatui/statusline"
)

const terminalTitleRunes = 160

// terminalTitleController is the sole terminal-title output authority. Set stages
// the title rendered during View; Write serializes a changed OSC update immediately
// before the corresponding Bubble Tea frame.
type terminalTitleController struct {
	mu        sync.Mutex
	output    io.Writer
	enabled   bool
	renderer  *statusline.TitleRenderer
	pending   string
	last      string
	wrote     bool
	debug     bool
	renderErr error
	writeErr  error
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled || c.renderErr != nil || c.writeErr != nil {
		return
	}
	title, err := c.renderer.Render(input)
	if err != nil {
		c.renderErr = fmt.Errorf("terminal_title.template: execute: %w", err)
		return
	}
	if c.debug {
		title = "DEBUG " + title
	}
	c.pending = sanitizeTerminalTitle(title)
}

func (c *terminalTitleController) flush() error {
	if c.pending == c.last || (c.pending == "" && !c.wrote) {
		return nil
	}
	if _, err := io.WriteString(c.output, "\x1b]0;"+c.pending+"\a"); err != nil {
		c.writeErr = err
		return err
	}
	c.last = c.pending
	if c.last != "" {
		c.wrote = true
	}
	return nil
}

func (c *terminalTitleController) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.renderErr != nil {
		return 0, c.renderErr
	}
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if err := c.flush(); err != nil {
		return 0, err
	}
	return c.output.Write(p)
}

func (c *terminalTitleController) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return c.writeErr
	}
	if c.enabled && c.wrote && c.last != "" {
		if _, err := io.WriteString(c.output, "\x1b]0;\a"); err != nil {
			return err
		}
		c.last = ""
	}
	return c.renderErr
}

func sanitizeTerminalTitle(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	space := true
	count := 0
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if unicode.IsSpace(r) {
			space = true
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
