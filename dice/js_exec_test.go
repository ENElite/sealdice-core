package dice

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
)

func TestJsExecHelperProcess(t *testing.T) {
	if os.Getenv("SEALDICE_JS_EXEC_HELPER") != "1" {
		return
	}

	separator := 0
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index + 1
			break
		}
	}
	if separator == 0 || separator >= len(os.Args) {
		os.Exit(2)
	}

	switch os.Args[separator] {
	case "success":
		stdin, _ := io.ReadAll(os.Stdin)
		cwd, _ := os.Getwd()
		extraArg := ""
		if separator+1 < len(os.Args) {
			extraArg = os.Args[separator+1]
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s|%s|%s|%s", extraArg, stdin, os.Getenv("SEALDICE_JS_EXEC_VALUE"), cwd)
	case "failure":
		_, _ = fmt.Fprint(os.Stderr, "expected failure")
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Second)
	case "large-output":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", 1024))
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func jsExecTestCommand(t *testing.T, mode string, extraArgs ...string) (string, []string) {
	t.Helper()
	program, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=TestJsExecHelperProcess", "--", mode}
	args = append(args, extraArgs...)
	return program, args
}

func TestRunJsCommandSuccess(t *testing.T) {
	program, args := jsExecTestCommand(t, "success", "argument with spaces")
	dir := t.TempDir()
	result, err := runJsCommand(context.Background(), program, args, JsExecOptions{
		Dir:   dir,
		Stdin: "input text",
		Env: map[string]string{
			"SEALDICE_JS_EXEC_HELPER": "1",
			"SEALDICE_JS_EXEC_VALUE":  "environment value",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	want := "argument with spaces|input text|environment value|" + dir
	if result.Stdout != want {
		t.Fatalf("stdout = %q, want %q", result.Stdout, want)
	}
}

func TestRunJsCommandFailureIsResult(t *testing.T) {
	program, args := jsExecTestCommand(t, "failure")
	result, err := runJsCommand(context.Background(), program, args, JsExecOptions{
		Env: map[string]string{"SEALDICE_JS_EXEC_HELPER": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.ExitCode != 7 || result.Stderr != "expected failure" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunJsCommandTimeout(t *testing.T) {
	program, args := jsExecTestCommand(t, "sleep")
	result, err := runJsCommand(context.Background(), program, args, JsExecOptions{
		Env:       map[string]string{"SEALDICE_JS_EXEC_HELPER": "1"},
		TimeoutMs: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.Success {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunJsCommandCancellation(t *testing.T) {
	program, args := jsExecTestCommand(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result, err := runJsCommand(ctx, program, args, JsExecOptions{
		Env: map[string]string{"SEALDICE_JS_EXEC_HELPER": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Cancelled || result.TimedOut || result.Success {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunJsCommandLimitsOutput(t *testing.T) {
	program, args := jsExecTestCommand(t, "large-output")
	result, err := runJsCommand(context.Background(), program, args, JsExecOptions{
		Env:            map[string]string{"SEALDICE_JS_EXEC_HELPER": "1"},
		MaxOutputBytes: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || !result.Truncated || len(result.Stdout) != 32 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSealExecReturnsPromise(t *testing.T) {
	program, args := jsExecTestCommand(t, "success", "from promise")
	loop := eventloop.NewEventLoop()
	loop.Start()
	defer loop.Terminate()

	done := make(chan string, 1)
	failure := make(chan error, 1)
	if !loop.RunOnLoop(func(vm *goja.Runtime) {
		vm.SetFieldNameMapper(goja.TagFieldNameMapper("jsbind", true))
		_ = vm.Set("sealExec", newJsExecFunction(vm, loop, context.Background(), nil))
		_ = vm.Set("testProgram", program)
		_ = vm.Set("testArgs", args)
		_ = vm.Set("done", func(value string) { done <- value })
		_ = vm.Set("failed", func(value string) { failure <- fmt.Errorf("promise rejected: %s", value) })
		script := `
			sealExec(testProgram, testArgs, {
				env: { SEALDICE_JS_EXEC_HELPER: "1" },
				stdin: "promise input"
			}).then(
				result => done(JSON.stringify(result)),
				error => failed(String(error))
			);
		`
		if _, err := vm.RunString(script); err != nil {
			failure <- err
		}
	}) {
		t.Fatal("failed to schedule JS test")
	}

	select {
	case value := <-done:
		if !strings.Contains(value, `"success":true`) || !strings.Contains(value, "from promise|promise input") {
			t.Fatalf("unexpected Promise result: %s", value)
		}
	case err := <-failure:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for seal.exec Promise")
	}
}
