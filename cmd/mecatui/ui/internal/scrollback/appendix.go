package scrollback

// AppendixSnapshot is part of the internal typed scrollback contract.
type AppendixSnapshot struct {
	ID               BlockID
	Files            []string
	PrecedingBlockID BlockID
}

// AppendixSnapshot is part of the internal typed scrollback contract.
func (c *Conversation) AppendixSnapshot() (AppendixSnapshot, bool) {
	if c.appendix == nil {
		return AppendixSnapshot{}, false
	}
	return AppendixSnapshot{
		ID: c.appendix.ID, Files: cloneStrings(c.appendix.Files), PrecedingBlockID: c.precedingBlockID(),
	}, true
}

// RecordFileChange is part of the internal typed scrollback contract.
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
