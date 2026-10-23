package dice

import (
	"strings"

	"github.com/dop251/goja"
)

// newJsBashFunction shares seal.exec's runner, including its concurrency slots
// and JSVM cancellation context. Bash runs inside the core process's host or
// container, with the same operating-system permissions as seal.exec.
func newJsBashFunction(vm *goja.Runtime, execute func(goja.FunctionCall) goja.Value) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		command, ok := call.Argument(0).Export().(string)
		if !ok || strings.TrimSpace(command) == "" || strings.ContainsRune(command, '\x00') {
			panic(vm.NewTypeError("seal.bash: command must be a non-empty string without NUL"))
		}
		options := call.Argument(1)
		if !goja.IsUndefined(options) && !goja.IsNull(options) {
			object, isObject := options.(*goja.Object)
			if !isObject || object.ClassName() != "Object" {
				panic(vm.NewTypeError("seal.bash: options must be an object"))
			}
		}
		return execute(goja.FunctionCall{
			This: call.This,
			Arguments: []goja.Value{
				vm.ToValue("bash"),
				vm.ToValue([]string{"--noprofile", "--norc", "-c", command}),
				options,
			},
		})
	}
}
