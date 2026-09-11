package shellcompat

import "testing"

func TestShellCompatibilityDiagnostic(t *testing.T) {
	tests := []struct {
		name    string
		shell   string
		command string
		wantErr bool
	}{
		{name: "sh test clause", shell: "/bin/sh", command: "[[ -n value ]]", wantErr: true},
		{name: "dash process substitution", shell: "/bin/dash", command: "cat <(echo value)", wantErr: true},
		{name: "sh array", shell: "/bin/sh", command: "values=(one two)", wantErr: true},
		{name: "sh ANSI-C quote", shell: "/bin/sh", command: "printf $'value\\n'", wantErr: true},
		{name: "bash permits Bash syntax", shell: "/bin/bash", command: "[[ -n value ]]"},
		{name: "unknown shell permits Bash syntax", shell: "/usr/local/bin/custom", command: "[[ -n value ]]"},
		{name: "quoted text is data", shell: "/bin/sh", command: "printf '%s\\n' '[[ value ]]'"},
		{name: "parse failure passes through", shell: "/bin/sh", command: "if then"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Check(tt.shell, tt.command); (err != nil) != tt.wantErr {
				t.Fatalf("Check(%q, %q) error = %v, want error %t", tt.shell, tt.command, err, tt.wantErr)
			}
		})
	}
}
