# JSVM 外部命令接口

`seal.exec` 允许受信任的 JS 插件异步启动 sealdice-core 之外的程序。它直接传递可执行文件和参数数组，不使用 shell。

```js
const result = await seal.exec(
  'program',
  ['--flag', 'argument with spaces'],
  {
    dir: '/work',
    env: { NAME: 'value' },
    stdin: 'input',
    timeoutMs: 120000,
    maxOutputBytes: 4194304,
  },
);
```

返回的 Promise 在进程完成、非零退出、超时或取消时 resolve：

```ts
interface ExecResult {
  success: boolean;
  exitCode: number;
  stdout: string;
  stderr: string;
  timedOut: boolean;
  cancelled: boolean;
  truncated: boolean;
  durationMs: number;
}
```

如果程序无法启动、参数不合法或同时运行的命令超过 8 个，Promise 会 reject。JSVM 关闭或重载时，仍在运行的命令会被取消。

默认超时为 120 秒，最大 1800 秒；标准输出和标准错误分别默认最多保留 4 MiB，最大 16 MiB。超过限制的内容仍会被读取并丢弃，同时将 `truncated` 置为 `true`。

## 安全边界

这是高权限接口。JS 插件能够以 sealdice-core 的系统账户启动任意可执行文件，也会默认继承核心进程的环境变量。只应安装可信插件，并建议在权限受限的系统账户或容器中运行海豹。

调用 `seal.exec` 时应把程序名和每个参数分别传入，避免再次引入 shell 拼接。需要运行不可信输入时，应同时限制可执行文件、工作目录、超时和输出大小。

## Bash 命令行

`seal.bash(command, options?)` 为可信 JS 插件提供 Bash 命令行，支持管道、重定向、多行脚本等语法。在容器中运行海豹时，命令就在同一容器内执行，以海豹进程的账户和权限运行；不会自动连接宿主机或其他容器。运行环境需要在 `PATH` 中安装 `bash`，不存在时 Promise reject。Windows 上也需要自行提供 Bash。

```js
const result = await seal.bash('pwd; printf "%s\\n" "$TASK_NAME" | tr a-z A-Z', {
  dir: '/work',
  env: { TASK_NAME: 'diagnostic' },
  timeoutMs: 10000,
  maxOutputBytes: 65536,
});
console.log(result.stdout, result.stderr, result.exitCode);
```

`command` 必须是非空且不含 NUL 的字符串，`options` 必须是对象；不合法时抛出参数错误。选项、Promise 返回值、非零退出处理、超时和输出限制与 `seal.exec` 相同，两者共享最多 8 个并发命令的限制。JSVM 重载或关闭时会取消命令；Unix 上同时终止进程组。每次调用启动独立的 `bash --noprofile --norc -c` 进程，`cd` 等状态不会保留到下一次调用，可用 `dir` 指定工作目录；不提供交互终端。

这与现有 `seal.exec` 使用相同的可信插件权限边界，不额外提升系统权限。插件若将此能力暴露为聊天指令，必须自行检查现有骰主权限（例如 `ctx.privilegeLevel >= 100`），不要向普通用户开放任意命令执行。Bash 会解释整个命令字符串；外部输入应通过 `env` 或 `stdin` 传入，并在脚本中引用变量，不能直接拼接进命令。无需 shell 语法时优先使用 `seal.exec` 的参数数组。
