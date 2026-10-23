package dice

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
)

func TestSealBashValidatesArguments(t *testing.T) {
	vm := goja.New()
	called := false
	_ = vm.Set("bash", newJsBashFunction(vm, func(_ goja.FunctionCall) goja.Value {
		called = true
		return goja.Undefined()
	}))
	for _, script := range []string{
		`bash()`, `bash(null)`, `bash(42)`, `bash({})`, `bash("")`,
		`bash("  ")`, `bash("hello\u0000world")`, `bash("pwd", [])`, `bash("pwd", "bad")`,
	} {
		if _, err := vm.RunString(script); err == nil {
			t.Errorf("expected argument error for %s", script)
		}
	}
	if called {
		t.Fatal("invalid arguments reached the process runner")
	}
}

func TestSealBashUsesSharedRunner(t *testing.T) {
	vm := goja.New()
	_ = vm.Set("bash", newJsBashFunction(vm, func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).String() != "bash" {
			t.Fatal("unexpected executable")
		}
		var args []string
		if err := vm.ExportTo(call.Argument(1), &args); err != nil {
			t.Fatal(err)
		}
		if strings.Join(args, "|") != "--noprofile|--norc|-c|printf hello | cat" {
			t.Fatalf("unexpected arguments: %q", args)
		}
		if call.Argument(2).ToObject(vm).Get("timeoutMs").ToInteger() != 50 {
			t.Fatal("options were not forwarded")
		}
		return vm.ToValue("shared result")
	}))
	value, err := vm.RunString(`bash("printf hello | cat", {timeoutMs: 50})`)
	if err != nil || value.String() != "shared result" {
		t.Fatalf("result = %v, err = %v", value, err)
	}
}

func TestSealBashExecution(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	for _, test := range []struct {
		name   string
		script string
		want   string
	}{
		{
			name: "shell and options",
			script: `bash('read -r input; printf "%s" "$input" | tr a-z A-Z; printf "%s" "$BASH_TEST_VALUE" >&2; exit 7', {
				stdin: "hello\n", env: {BASH_TEST_VALUE: "diagnostic"}
			}).then(r => done([r.exitCode, r.stdout, r.stderr, r.success].join('|')), failed);`,
			want: "7|HELLO|diagnostic|false",
		},
		{
			name: "output limit",
			script: `bash('printf 123456789', {maxOutputBytes: 4})
				.then(r => done([r.stdout, r.truncated].join('|')), failed);`,
			want: "1234|true",
		},
		{
			name: "timeout",
			script: `bash('sleep 5', {timeoutMs: 50})
				.then(r => done([r.success, r.timedOut].join('|')), failed);`,
			want: "false|true",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			loop := eventloop.NewEventLoop()
			loop.Start()
			defer loop.Terminate()
			done := make(chan string, 1)
			failure := make(chan error, 1)
			loop.RunOnLoop(func(vm *goja.Runtime) {
				vm.SetFieldNameMapper(goja.TagFieldNameMapper("jsbind", true))
				_ = vm.Set("bash", newJsBashFunction(vm, newJsExecFunction(vm, loop, context.Background(), nil)))
				_ = vm.Set("done", func(value string) { done <- value })
				_ = vm.Set("failed", func(value string) { failure <- fmt.Errorf("promise rejected: %s", value) })
				if _, err := vm.RunString(test.script); err != nil {
					failure <- err
				}
			})
			select {
			case value := <-done:
				if value != test.want {
					t.Fatalf("result = %q, want %q", value, test.want)
				}
			case err := <-failure:
				t.Fatal(err)
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for Bash result")
			}
		})
	}
}
