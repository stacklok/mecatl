package scrollback

// AppendixSnapshot is a detached description of the changed-files appendix. ID
// is allocated once when the first non-empty path is recorded; PrecedingBlockID
// identifies the final ordinary card at snapshot time, or zero when there is none.
type AppendixSnapshot struct {
	ID               BlockID
	Files            []string
	PrecedingBlockID BlockID
}

// AppendixSnapshot returns the current changed-files appendix and true, or false
// if no non-empty file path has been recorded. Its Files slice is detached from
// the Conversation.
func (c *Conversation) AppendixSnapshot() (AppendixSnapshot, bool) {
	if c.appendix == nil {
		return AppendixSnapshot{}, false
	}
	return AppendixSnapshot{
		ID: c.appendix.ID, Files: cloneStrings(c.appendix.Files), PrecedingBlockID: c.precedingBlockID(),
	}, true
}

// RecordFileChange records path in the conversation's changed-files appendix and
// returns that appendix's stable ID. Empty paths return zero. Repeated paths do
// not add another entry and return the existing appendix ID when it exists.
func (c *Conversation) RecordFileChange(path string) BlockID {
	if path == "" {
		return 0
	}
	if c.seen == nil {
		c.seen = map[string]struct{}{}
	}
	if _, ok := c.seen[path]; ok {
		if c.appendix == nil {
			return 0
		}
		return c.appendix.ID
	}
	c.seen[path] = struct{}{}
	if c.appendix == nil {
		c.nextID++
		c.appendix = &AppendixSnapshot{ID: c.nextID}
	}
	c.appendix.Files = append(c.appendix.Files, path)
	return c.appendix.ID
}

func (c *Conversation) precedingBlockID() BlockID {
	if len(c.cards) == 0 {
		return 0
	}
	return c.cards[len(c.cards)-1].id
}
