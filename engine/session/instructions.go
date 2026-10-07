package session

// InstructionScope is an ephemeral examined source-relative instruction scope.
type InstructionScope struct {
	SourceID, Directory, File, Text string
	Partial, Omitted                bool
	Examined                        bool
	Unavailable                     bool
}

// InstructionSnapshot holds the live session's automatically discovered guidance.
// It is not part of the persisted session snapshot or conversation.
type InstructionSnapshot struct {
	Directories        []string
	Scopes             []InstructionScope
	DiscoveryExhausted bool
}

// MetadataBytes reports the snapshot's metadata-budget usage.
func (s InstructionSnapshot) MetadataBytes() int {
	used := 0
	for _, dir := range s.Directories {
		used += len(dir) + 1
	}
	for _, scope := range s.Scopes {
		used += len(scope.SourceID) + len(scope.Directory) + len(scope.File) + 1
	}
	return used
}

func (s InstructionSnapshot) clone() InstructionSnapshot {
	s.Directories = append([]string(nil), s.Directories...)
	s.Scopes = append([]InstructionScope(nil), s.Scopes...)
	return s
}

// InstructionSnapshot returns a detached view of the live discovery state.
func (s *Session) InstructionSnapshot() InstructionSnapshot { return s.instructionSnapshot.clone() }

// ReplaceInstructionSnapshot replaces live discovery state without aliasing its caller.
func (s *Session) ReplaceInstructionSnapshot(snapshot InstructionSnapshot) {
	s.instructionSnapshot = snapshot.clone()
}
