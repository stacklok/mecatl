// Package agentimport converts local Codex and Claude Code artifacts into
// provider-neutral Mecatl data. Import is deliberately lossy: only user and
// assistant text enters conversation history. Provider-private reasoning and
// tool-call wire records cannot be replayed safely by another provider.
package agentimport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// Source identifies an external agent transcript format.
type Source string

const (
	// SourceCodex identifies the Codex external agent transcript format.
	SourceCodex Source = "codex"
	// SourceClaudeCode identifies the Claude Code external agent transcript format.
	SourceClaudeCode Source = "claude-code"
	maxRecordBytes          = 16 << 20
)

// Transcript is the provider-neutral subset of an external session.
type Transcript struct {
	ExternalID string
	Workspace  string
	CreatedAt  time.Time
	Title      string
	Messages   []session.Message
}

// Parse reads one JSONL transcript.
func Parse(source Source, r io.Reader) (Transcript, error) {
	switch source {
	case SourceCodex:
		return parseCodex(r)
	case SourceClaudeCode:
		return parseClaudeCode(r)
	default:
		return Transcript{}, fmt.Errorf("unsupported import source %q (want codex or claude-code)", source)
	}
}

type jsonlRecord struct {
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	IsCompact   bool            `json:"isCompactSummary"`
	Payload     json.RawMessage `json:"payload"`
	Message     json.RawMessage `json:"message"`
	CustomTitle string          `json:"customTitle"`
	Title       string          `json:"title"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func scanJSONL(r io.Reader, visit func(int, jsonlRecord) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxRecordBytes)
	line := 0
	for sc.Scan() {
		line++
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var rec jsonlRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return fmt.Errorf("decode JSONL line %d: %w", line, err)
		}
		if err := visit(line, rec); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan JSONL (records are limited to %d MiB): %w", maxRecordBytes>>20, err)
	}
	return nil
}

func parseCodex(r io.Reader) (Transcript, error) {
	var out Transcript
	var canonical, fallback []session.Message
	err := scanJSONL(r, func(_ int, rec jsonlRecord) error {
		setEarliestTime(&out.CreatedAt, rec.Timestamp)
		switch rec.Type {
		case "session_meta":
			var p struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
				Cwd       string `json:"cwd"`
				Timestamp string `json:"timestamp"`
			}
			if err := json.Unmarshal(rec.Payload, &p); err != nil {
				return fmt.Errorf("decode Codex session metadata: %w", err)
			}
			out.ExternalID = firstNonEmpty(p.ID, p.SessionID)
			out.Workspace = p.Cwd
			setEarliestTime(&out.CreatedAt, p.Timestamp)
		case "response_item":
			var p struct {
				Type    string      `json:"type"`
				Role    string      `json:"role"`
				Content []textBlock `json:"content"`
			}
			if err := json.Unmarshal(rec.Payload, &p); err != nil {
				return fmt.Errorf("decode Codex response item: %w", err)
			}
			if p.Type == "message" {
				appendTextMessage(&canonical, p.Role, contentText(p.Content))
			}
		case "event_msg":
			var p struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Text    string `json:"text"`
			}
			if err := json.Unmarshal(rec.Payload, &p); err != nil {
				return fmt.Errorf("decode Codex event message: %w", err)
			}
			switch p.Type {
			case "user_message":
				appendTextMessage(&fallback, "user", firstNonEmpty(p.Message, p.Text))
			case "agent_message":
				appendTextMessage(&fallback, "assistant", firstNonEmpty(p.Message, p.Text))
			}
		}
		return nil
	})
	if err != nil {
		return Transcript{}, err
	}
	out.Messages = canonical
	if len(out.Messages) == 0 {
		out.Messages = fallback
	}
	finishTranscript(&out)
	return out, nil
}

func parseClaudeCode(r io.Reader) (Transcript, error) {
	var out Transcript
	err := scanJSONL(r, func(_ int, rec jsonlRecord) error {
		if rec.IsSidechain || rec.IsMeta || rec.IsCompact {
			return nil
		}
		if out.ExternalID == "" {
			out.ExternalID = rec.SessionID
		}
		if out.Workspace == "" {
			out.Workspace = rec.Cwd
		}
		setEarliestTime(&out.CreatedAt, rec.Timestamp)
		if out.Title == "" {
			out.Title = firstNonEmpty(rec.CustomTitle, rec.Title)
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			return nil
		}
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(rec.Message, &msg); err != nil {
			return fmt.Errorf("decode Claude Code message: %w", err)
		}
		text, err := claudeContentText(msg.Content)
		if err != nil {
			return err
		}
		appendTextMessage(&out.Messages, msg.Role, text)
		return nil
	})
	if err != nil {
		return Transcript{}, err
	}
	finishTranscript(&out)
	return out, nil
}

func claudeContentText(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var blocks []textBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("decode Claude Code message content: %w", err)
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func contentText(blocks []textBlock) string {
	var parts []string
	for _, block := range blocks {
		if (block.Type == "input_text" || block.Type == "output_text" || block.Type == "text") && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func appendTextMessage(dst *[]session.Message, role, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	var message session.Message
	switch role {
	case "user":
		message = session.NewUserMessage(text)
	case "assistant":
		message = session.NewAssistantMessage(text, "", nil)
	default:
		return
	}
	// Tool-only records are deliberately omitted. Coalescing the text records on
	// either side keeps the resulting provider-neutral history well formed.
	// Message is an immutable value object, so coalescing replaces the last
	// element with a freshly constructed message rather than mutating its Text.
	if n := len(*dst); n > 0 && (*dst)[n-1].Role == message.Role {
		coalesced := message
		coalesced.Text = (*dst)[n-1].Text + "\n\n" + message.Text
		(*dst)[n-1] = coalesced
		return
	}
	*dst = append(*dst, message)
}

func finishTranscript(t *Transcript) {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	if strings.TrimSpace(t.Title) == "" {
		for _, msg := range t.Messages {
			if msg.Role == session.RoleUser {
				t.Title = session.ClampTitle(msg.Text)
				break
			}
		}
	} else {
		t.Title = session.ClampTitle(t.Title)
	}
}

func setEarliestTime(dst *time.Time, value string) {
	if value == "" {
		return
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil && (dst.IsZero() || parsed.Before(*dst)) {
		*dst = parsed
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
