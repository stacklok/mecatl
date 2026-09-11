package governance

import (
	"reflect"
	"testing"
)

func TestSplitCommands(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace", "   ", nil},
		{"single", "git status", []string{"git status"}},
		{
			name: "and-operator (doc 08 #10)",
			in:   "git status && rm -rf /",
			want: []string{"git status", "rm -rf /"},
		},
		{"or-operator", "make || echo fail", []string{"make", "echo fail"}},
		{"semicolon", "cd /tmp; ls", []string{"cd /tmp", "ls"}},
		{"pipe", "cat f | grep x", []string{"cat f", "grep x"}},
		{
			name: "mixed",
			in:   "a && b; c | d || e",
			want: []string{"a", "b", "c", "d", "e"},
		},
		{
			name: "operator in single quotes is literal",
			in:   "echo 'a && b' && ls",
			want: []string{"echo 'a && b'", "ls"},
		},
		{
			name: "operator in double quotes is literal",
			in:   `echo "x | y" | wc`,
			want: []string{`echo "x | y"`, "wc"},
		},
		{"trailing operator", "ls &&", []string{"ls"}},
		{"newline separates", "ls\nrm -rf build", []string{"ls", "rm -rf build"}},
		{"carriage return newline", "ls\r\nrm x", []string{"ls", "rm x"}},
		{"single ampersand background", "ls & rm -rf build", []string{"ls", "rm -rf build"}},
		{
			name: "newline inside double quotes is literal",
			in:   "echo \"a\nb\"\nls",
			want: []string{"echo \"a\nb\"", "ls"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitCommands(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SplitCommands(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCanonicalize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"timeout stripped", "timeout 5 rm x", "rm x"},
		{"timeout with signal flag+value", "timeout -s KILL 5 rm x", "rm x"},
		{"timeout long flag=value", "timeout --signal=KILL 5 rm x", "rm x"},
		{"nice stripped", "nice -n 10 cat f", "cat f"},
		{"env stripped", "env FOO=bar ls", "FOO=bar ls"},
		{"stdbuf stripped", "stdbuf -oL grep x f", "grep x f"},
		{"ionice stripped", "ionice -c 3 cat f", "cat f"},
		{"time stripped", "time ls", "ls"},
		{"nested wrappers", "timeout 5 nice -n 5 rm x", "rm x"},
		{"no wrapper unchanged", "rm x", "rm x"},

		// Re-entrant launchers must NOT be stripped (doc 08 §10 backdoors).
		{"docker exec NOT stripped", "docker exec foo rm x", "docker exec foo rm x"},
		{"npx NOT stripped", "npx some-tool", "npx some-tool"},
		{"devbox run NOT stripped", "devbox run rm x", "devbox run rm x"},
		{"sudo NOT stripped", "sudo rm x", "sudo rm x"},

		{"bare wrapper left as-is", "env", "env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Canonicalize(tt.in); got != tt.want {
				t.Fatalf("Canonicalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestReadOnlyShell(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"ls", "ls", true},
		{"cat", "cat file.txt", true},
		{"grep", "grep needle haystack", true},
		{"find", "find . -name x", true},
		{"rg", "rg pattern", true},
		{"head", "head -n 5 f", true},
		{"tail", "tail f", true},
		{"pwd", "pwd", true},
		{"git status", "git status", true},
		{"git log", "git log --oneline", true},
		{"git diff", "git diff HEAD", true},
		{"read-only compound", "git status && ls", true},
		{"wrapped read-only", "timeout 5 cat f", true},

		{"rm", "rm -rf /", false},
		{"mv", "mv a b", false},
		{"mkdir", "mkdir d", false},
		{"redirection", "echo hi > f", false},
		{"append redirection", "ls >> f", false},
		{"git commit", "git commit -m x", false},
		{"compound with rm denies", "git status && rm x", false},
		{"unknown command", "frobnicate", false},
		{"empty", "", false},

		// Verbs that mutate or execute through their own arguments (no shell
		// redirection) must NOT be classified read-only.
		{"awk system exec", `awk 'BEGIN{system("rm -rf /")}' /dev/null`, false},
		{"awk not read-only", "awk '{print $1}' f", false},
		{"sed in-place", "sed -i s/a/b/ file", false},
		{"sed in-place suffix", "sed -i.bak s/a/b/ secret.txt", false},
		{"sed not read-only", "sed s/a/b/ f", false},
		{"find -delete", "find . -name x -delete", false},
		{"find -exec", "find . -exec echo {} +", false},
		{"find -fprintf", "find . -fprintf out.txt %p", false},
		{"find -fls", "find . -fls out.txt", false},
		{"sort -o", "sort -o out.txt in.txt", false},
		{"sort -o trailing", "sort file -o out.txt", false},
		{"sort --output=", "sort --output=out.txt in.txt", false},
		{"git config alias exec", "git config alias.x '!rm -rf /'", false},
		{"git config pager", `git config core.pager '!sh -c "x"'`, false},

		// Benign uses of the guarded verbs stay read-only.
		{"find benign", "find . -name x", true},
		{"sort benign", "sort file.txt", true},

		// Finding 1: substitution / subshell grouping / newline / background
		// must all defeat the read-only classification (fail safe).
		{"command substitution", "cat $(rm x)", false},
		{"command substitution in echo", "echo ok $(rm -rf build)", false},
		{"backtick substitution", "echo `rm -rf build`", false},
		{"process substitution", "diff <(rm x) f", false},
		{"subshell grouping", "ls;(rm -rf build)", false},
		{"brace grouping", "ls; { rm x; }", false},
		{"newline smuggles rm", "ls\nrm x", false},
		{"background smuggles rm", "ls & rm x", false},
		{"param expansion is not read-only", "cat ${HOME}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReadOnlyShell(tt.in); got != tt.want {
				t.Fatalf("ReadOnlyShell(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestHasSubstitutionOrGrouping(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain command", "rm -rf build", false},
		{"command substitution", "echo $(rm x)", true},
		{"backtick", "echo `rm x`", true},
		{"process substitution in", "diff <(cat x) y", true},
		{"process substitution out", "tee >(cat) ", true},
		{"subshell", "(rm x)", true},
		{"brace group", "{ rm x; }", true},
		{"param expansion", "echo ${HOME}", true},
		{"substitution inside double quotes still flagged", `echo "$(rm x)"`, true},
		{"backtick inside double quotes flagged", "echo \"`rm x`\"", true},
		{"paren inside single quotes is literal", "echo '(not a subshell)'", false},
		{"paren inside double quotes is literal", `echo "(not a subshell)"`, false},
		{"dollar without paren is fine", "echo $HOME", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasSubstitutionOrGrouping(tt.in); got != tt.want {
				t.Fatalf("HasSubstitutionOrGrouping(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
