package governance

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ShellCompatibilityError returns bounded POSIX portability feedback for a
// model-facing command. It is deliberately neither an authorization decision nor
// a shell emulator: it only recognizes selected Bash AST nodes when the trusted
// configured shell path names sh or dash. Parse failures and every other shell
// basename leave the exact command bytes for the runner unchanged.
func ShellCompatibilityError(shellPath, command string) error {
	if shell := filepath.Base(shellPath); shell != "sh" && shell != "dash" {
		return nil
	}

	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}

	var nonPortable string
	syntax.Walk(file, func(node syntax.Node) bool {
		if nonPortable != "" {
			return false
		}
		switch node := node.(type) {
		case *syntax.TestClause:
			nonPortable = "[[ ... ]] test clauses"
		case *syntax.ProcSubst:
			nonPortable = "process substitution"
		case *syntax.ArrayExpr:
			nonPortable = "array expressions"
		case *syntax.SglQuoted:
			if node.Dollar {
				nonPortable = "ANSI-C quoted strings"
			}
		}
		return true
	})
	if nonPortable == "" {
		return nil
	}
	return shellCompatibilityError{construct: nonPortable}
}

type shellCompatibilityError struct {
	construct string
}

func (e shellCompatibilityError) Error() string {
	return "Shell compatibility diagnostic: " + e.construct + " are not portable to the configured sh/dash shell"
}
