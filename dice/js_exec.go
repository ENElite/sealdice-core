package dice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
	"go.uber.org/zap"
)

const (
	jsExecDefaultTimeoutMs     int64 = 120_000
	jsExecMaximumTimeoutMs     int64 = 1_800_000
	jsExecDefaultMaxOutputByte int64 = 4 * 1024 * 1024
	jsExecMaximumMaxOutputByte int64 = 16 * 1024 * 1024
	jsExecMaximumConcurrent          = 8
)

// JsExecOptions controls a process started through seal.exec.
//
// The executable and its arguments are always passed directly to os/exec. A
// shell is never involved, so callers do not need shell-specific escaping and
// untrusted arguments cannot introduce extra shell commands.
type JsExecOptions struct {
	Dir            string            `jsbind:"dir"`
	Env            map[string]string `jsbind:"env"`
	Stdin          string            `jsbind:"stdin"`
	TimeoutMs      int64             `jsbind:"timeoutMs"`
	MaxOutputBytes int64             `jsbind:"maxOutputBytes"`
}

// JsExecResult is the settled value of the Promise returned by seal.exec.
// Non-zero exit statuses and timeouts are normal results so plugins can inspect
// stdout and stderr. Failures that prevent a process from starting reject the
// Promise instead.
type JsExecResult struct {
	Success    bool   `jsbind:"success"`
	ExitCode   int    `jsbind:"exitCode"`
	Stdout     string `jsbind:"stdout"`
	Stderr     string `jsbind:"stderr"`
	TimedOut   bool   `jsbind:"timedOut"`
	Cancelled  bool   `jsbind:"cancelled"`
	Truncated  bool   `jsbind:"truncated"`
	DurationMs int64  `jsbind:"durationMs"`
}

type jsExecLimitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *jsExecLimitedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining < len(data) {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	if originalLength > 0 && originalLength > remaining {
		b.truncated = true
	}
	// Report the original length so os/exec can continue draining the pipe
	// without retaining unbounded output in memory.
	return originalLength, nil
}

func (b *jsExecLimitedBuffer) String() string {
	return b.buffer.String()
}

func jsExecTimeout(timeoutMs int64) time.Duration {
	if timeoutMs <= 0 {
		timeoutMs = jsExecDefaultTimeoutMs
	}
	if timeoutMs > jsExecMaximumTimeoutMs {
		timeoutMs = jsExecMaximumTimeoutMs
	}
	return time.Duration(timeoutMs) * time.Millisecond
}

func jsExecOutputLimit(maxOutputBytes int64) int {
	if maxOutputBytes <= 0 {
		maxOutputBytes = jsExecDefaultMaxOutputByte
	}
	if maxOutputBytes > jsExecMaximumMaxOutputByte {
		maxOutputBytes = jsExecMaximumMaxOutputByte
	}
	return int(maxOutputBytes)
}

func jsExecEnvironment(overrides map[string]string) ([]string, error) {
	if len(overrides) == 0 {
		return nil, nil
	}

	environment := os.Environ()
	for key, value := range overrides {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return nil, fmt.Errorf("invalid environment variable name %q", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("environment variable %q contains NUL", key)
		}

		matched := false
		for index, item := range environment {
			currentKey, _, _ := strings.Cut(item, "=")
			keysMatch := currentKey == key
			if runtime.GOOS == "windows" {
				keysMatch = strings.EqualFold(currentKey, key)
			}
			if keysMatch {
				environment[index] = key + "=" + value
				matched = true
			}
		}
		if !matched {
			environment = append(environment, key+"="+value)
		}
	}
	return environment, nil
}

func runJsCommand(parent context.Context, program string, args []string, options JsExecOptions) (JsExecResult, error) {
	result := JsExecResult{ExitCode: -1}
	if strings.TrimSpace(program) == "" || strings.ContainsRune(program, '\x00') {
		return result, errors.New("program must be a non-empty executable name or path")
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return result, errors.New("command argument contains NUL")
		}
	}

	environment, err := jsExecEnvironment(options.Env)
	if err != nil {
		return result, err
	}

	ctx, cancel := context.WithTimeout(parent, jsExecTimeout(options.TimeoutMs))
	defer cancel()

	command := exec.CommandContext(ctx, program, args...) //nolint:gosec // seal.exec is an explicitly privileged plugin API.
	configureJsExecCommand(command)
	command.Dir = options.Dir
	command.Stdin = strings.NewReader(options.Stdin)
	command.WaitDelay = 2 * time.Second
	if environment != nil {
		command.Env = environment
	}

	limit := jsExecOutputLimit(options.MaxOutputBytes)
	stdout := &jsExecLimitedBuffer{limit: limit}
	stderr := &jsExecLimitedBuffer{limit: limit}
	command.Stdout = stdout
	command.Stderr = stderr

	startedAt := time.Now()
	err = command.Run()
	result.DurationMs = time.Since(startedAt).Milliseconds()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.Truncated = stdout.truncated || stderr.truncated

	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if err == nil {
		result.Success = true
		return result, nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		result.TimedOut = errors.Is(ctxErr, context.DeadlineExceeded)
		result.Cancelled = errors.Is(ctxErr, context.Canceled)
		return result, nil
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return result, nil
	}
	return result, err
}

func newJsExecFunction(vm *goja.Runtime, loop *eventloop.EventLoop, parent context.Context, logger *zap.SugaredLogger) func(goja.FunctionCall) goja.Value {
	concurrencySlots := make(chan struct{}, jsExecMaximumConcurrent)

	return func(call goja.FunctionCall) goja.Value {
		programValue := call.Argument(0)
		if goja.IsUndefined(programValue) || goja.IsNull(programValue) {
			panic(vm.NewTypeError("seal.exec: program is required"))
		}
		program := programValue.String()

		var args []string
		argsValue := call.Argument(1)
		if !goja.IsUndefined(argsValue) && !goja.IsNull(argsValue) {
			if err := vm.ExportTo(argsValue, &args); err != nil {
				panic(vm.NewTypeError("seal.exec: args must be an array of strings: %s", err))
			}
		}

		var options JsExecOptions
		optionsValue := call.Argument(2)
		if !goja.IsUndefined(optionsValue) && !goja.IsNull(optionsValue) {
			if err := vm.ExportTo(optionsValue, &options); err != nil {
				panic(vm.NewTypeError("seal.exec: invalid options: %s", err))
			}
		}

		promise, resolve, reject := vm.NewPromise()
		if err := parent.Err(); err != nil {
			_ = reject(err)
			return vm.ToValue(promise)
		}
		select {
		case concurrencySlots <- struct{}{}:
		default:
			_ = reject(fmt.Errorf("seal.exec: at most %d commands may run concurrently", jsExecMaximumConcurrent))
			return vm.ToValue(promise)
		}

		go func() {
			defer func() { <-concurrencySlots }()
			result, err := runJsCommand(parent, program, args, options)
			scheduled := loop.RunOnLoop(func(_ *goja.Runtime) {
				if err != nil {
					_ = reject(err)
					return
				}
				_ = resolve(result)
			})
			if !scheduled && logger != nil {
				logger.Debugf("seal.exec result discarded because the JS event loop was terminated: %s", program)
			}
		}()

		return vm.ToValue(promise)
	}
}
