package fstools

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func rawOperandCall(name, args string) session.ToolCall {
	return session.NewToolCall("operand-call", name, []byte(args))
}

func TestLocalFileOperandsReadExecution(t *testing.T) {
	for _, tt := range []struct {
		args      string
		want      string
		wantError bool
	}{
		{`{"path":"a"}`, "a", false},
		{`{"Path":"b"}`, "b", false},
		{`{"path":"a","Path":"b"}`, "b", false},
		{`{"Path":"b","path":"a"}`, "a", false},
		{`{"path":"a","path":"b"}`, "b", false},
		{`{"path":"b","path":"a"}`, "a", false},
		{`{"path":"a","path":null}`, "a", false},
		{`{"path":null,"path":"a"}`, "a", false},
		{`{"path":1,"path":"a"}`, "", true},
		{`{"path":"a","path":1}`, "", true},
		{`{"path":null}`, "", true},
		{`{}`, "", true},
		{`{"path":""}`, "", true},
		{`{"path":"a","offset":"bad"}`, "a", true},
	} {
		t.Run(tt.args, func(t *testing.T) {
			ws := memfs.NewWorkspace("/")
			seed(t, ws, "a", "MARKER_A")
			seed(t, ws, "b", "MARKER_B")
			call := rawOperandCall("Read", tt.args)
			var want []string
			if tt.want != "" {
				want = []string{tt.want}
			}
			if got := tool.LocalFileOperands(call.Name, call.Args); !slices.Equal(got, want) {
				t.Fatalf("operands=%q want=%q", got, want)
			}
			result := exec(t, ReadTool{}, call, ws)
			if result.IsError != tt.wantError {
				t.Fatalf("Read=%+v wantError=%v", result, tt.wantError)
			}
			if !tt.wantError {
				data, err := ws.Read(context.Background(), tt.want)
				if err != nil || result.Content != "     1\t"+string(data)+"\n" {
					t.Fatalf("Read=%+v expected content=%q err=%v", result, data, err)
				}
			}
		})
	}
}

func TestLocalFileOperandsPathToolExecution(t *testing.T) {
	for _, tt := range []struct {
		tool tool.Tool
		args string
	}{
		{ReadTool{}, `{"path":"a","Path":"b"}`},
		{EditTool{}, `{"path":"a","Path":"b","old_string":"MARKER_B","new_string":"changed"}`},
		{WriteTool{}, `{"path":"a","Path":"b","content":"changed"}`},
		{RemoveTool{}, `{"path":"a","Path":"b"}`},
		{ListDirTool{}, `{"path":"a","Path":"b"}`},
	} {
		t.Run(tt.tool.Spec().Name, func(t *testing.T) {
			ws := memfs.NewWorkspace("/")
			name := tt.tool.Spec().Name
			if name == "ListDir" {
				seed(t, ws, "a/WRONG", "a")
				seed(t, ws, "b/RIGHT", "b")
			} else {
				seed(t, ws, "a", "MARKER_A")
				seed(t, ws, "b", "MARKER_B")
				if result := exec(t, ReadTool{}, rawOperandCall("Read", `{"path":"b"}`), ws); result.IsError {
					t.Fatal(result)
				}
			}
			call := rawOperandCall(name, tt.args)
			if got := tool.LocalFileOperands(name, call.Args); !slices.Equal(got, []string{"b"}) {
				t.Fatalf("operands=%q", got)
			}
			result := exec(t, tt.tool, call, ws)
			if result.IsError {
				t.Fatal(result)
			}
			switch name {
			case "ListDir":
				if result.Content != "RIGHT\n" {
					t.Fatal(result)
				}
			case "Read":
				if result.Content != "     1\tMARKER_B\n" {
					t.Fatal(result)
				}
			default:
				data, err := ws.Read(context.Background(), "b")
				if name == "Remove" {
					if !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("removed b still exists: %q err=%v", data, err)
					}
				} else if err != nil || string(data) != "changed" {
					t.Fatalf("b=%q err=%v", data, err)
				}
				data, err = ws.Read(context.Background(), "a")
				if err != nil || string(data) != "MARKER_A" {
					t.Fatalf("unselected a changed: %q err=%v", data, err)
				}
			}
		})
	}
}

func TestLocalFileOperandsCopyMoveExecution(t *testing.T) {
	for _, tl := range []tool.Tool{CopyTool{}, MoveTool{}} {
		t.Run(tl.Spec().Name, func(t *testing.T) {
			for _, tt := range []struct {
				args                string
				source, destination string
			}{
				{`{"source":"a","destination":"x"}`, "a", "x"},
				{`{"source":"a","Source":"b","destination":"x","Destination":"y"}`, "b", "y"},
				{`{"Source":"b","source":"a","Destination":"y","destination":"x"}`, "a", "x"},
				{`{"source":"a","source":"b","destination":"x","destination":"y"}`, "b", "y"},
				{`{"source":"b","source":"a","destination":"y","destination":"x"}`, "a", "x"},
				{`{"source":"a","source":null,"destination":"x","destination":null}`, "a", "x"},
				{`{"source":null,"source":"a","destination":null,"destination":"x"}`, "a", "x"},
				{`{"ſource":"b","destination":"y"}`, "b", "y"},
				{`{"source":1,"source":"a","destination":"x"}`, "", ""},
				{`{"source":"a","source":1,"destination":"x"}`, "", ""},
				{`{"source":"a","destination":1,"destination":"x"}`, "", ""},
				{`{"source":"a","destination":"x","destination":1}`, "", ""},
				{`{"source":"a"}`, "", ""},
				{`{"destination":"x"}`, "", ""},
				{`{"source":"","destination":"x"}`, "", ""},
				{`{"source":"a","destination":""}`, "", ""},
			} {
				t.Run(tt.args, func(t *testing.T) {
					ws := memfs.NewWorkspace("/")
					seed(t, ws, "a", "MARKER_a")
					seed(t, ws, "b", "MARKER_b")
					call := rawOperandCall(tl.Spec().Name, tt.args)
					var want []string
					if tt.source != "" {
						want = []string{tt.source, tt.destination}
					}
					if got := tool.LocalFileOperands(call.Name, call.Args); !slices.Equal(got, want) {
						t.Fatalf("operands=%q want=%q", got, want)
					}
					result := exec(t, tl, call, ws)
					if result.IsError != (tt.source == "") {
						t.Fatalf("result=%+v want operands=%q", result, want)
					}
					for _, path := range []string{"a", "b", "x", "y"} {
						data, err := ws.Read(context.Background(), path)
						switch {
						case path == tt.source && call.Name == "Move", (path == "x" || path == "y") && path != tt.destination:
							if !errors.Is(err, fs.ErrNotExist) {
								t.Fatalf("%s should be absent: %q err=%v", path, data, err)
							}
						default:
							marker := "MARKER_" + path
							if path == tt.destination {
								marker = "MARKER_" + tt.source
							}
							if err != nil || string(data) != marker {
								t.Fatalf("%s=%q want=%q err=%v", path, data, marker, err)
							}
						}
					}
				})
			}
		})
	}
}

type operandRecordingRunner struct{ commands []string }

func (r *operandRecordingRunner) Run(_ context.Context, command string) (tool.CommandResult, error) {
	r.commands = append(r.commands, command)
	return tool.CommandResult{}, nil
}

func TestLocalFileOperandsShellExecution(t *testing.T) {
	for _, tt := range []struct{ args, command, path string }{
		{`{"command":"./a.sh"}`, "./a.sh", "./a.sh"},
		{`{"Command":"./b.sh"}`, "./b.sh", "./b.sh"},
		{`{"command":"./a.sh","Command":"./b.sh"}`, "./b.sh", "./b.sh"},
		{`{"Command":"./b.sh","command":"./a.sh"}`, "./a.sh", "./a.sh"},
		{`{"command":"./a.sh","command":"./b.sh"}`, "./b.sh", "./b.sh"},
		{`{"command":"./b.sh","command":"./a.sh"}`, "./a.sh", "./a.sh"},
		{`{"command":"./a.sh","command":null}`, "./a.sh", "./a.sh"},
		{`{"command":null,"command":"./a.sh"}`, "./a.sh", "./a.sh"},
		{`{"command":1,"command":"./a.sh"}`, "", ""},
		{`{"command":"./a.sh","command":1}`, "", ""},
		{`{}`, "", ""},
		{`{"command":""}`, "", ""},
		{`{"command":"sh a.sh"}`, "sh a.sh", "a.sh"},
		{`{"command":"bash ./a.sh"}`, "bash ./a.sh", "./a.sh"},
		{`{"command":"dash ./a.sh"}`, "dash ./a.sh", "./a.sh"},
		{`{"command":"echo ./a.sh"}`, "echo ./a.sh", ""},
		{`{"command":"./a.sh; echo b"}`, "./a.sh; echo b", ""},
		{`{"command":"bash -c ./a.sh"}`, "bash -c ./a.sh", ""},
	} {
		t.Run(tt.args, func(t *testing.T) {
			call := rawOperandCall("Shell", tt.args)
			var want []string
			if tt.path != "" {
				want = []string{tt.path}
			}
			if got := tool.LocalFileOperands(call.Name, call.Args); !slices.Equal(got, want) {
				t.Fatalf("operands=%q want=%q", got, want)
			}
			runner := &operandRecordingRunner{}
			result := execWithRunner(t, NewShellTool(), call, memfs.NewWorkspace("/"), runner)
			var commands []string
			if tt.command != "" {
				commands = []string{tt.command}
			}
			if result.IsError != (tt.command == "") || !slices.Equal(runner.commands, commands) {
				t.Fatalf("result=%+v commands=%q want=%q", result, runner.commands, commands)
			}
		})
	}
}
