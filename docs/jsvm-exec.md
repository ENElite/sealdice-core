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

调用方必须把程序名和每个参数分别传入；不要用 `sh -c`、`cmd /c` 或 PowerShell 再次引入 shell 拼接。需要运行不可信输入时，应同时限制可执行文件、工作目录、超时和输出大小。
