# SealDice MCP 管理接口

服务启动后，MCP Streamable HTTP 地址为 `https://你的骰子地址/sd-api/v2/mcp`。
与 WebUI 使用同一组管理 token、同一套权限与展示模式限制，无单独 MCP 密码。
先在 WebUI 使用密码（或已经绑定的 OAuth）登录，取该会话的 token，配置 agent 的 HTTP MCP 客户端：

```json
{
  "mcpServers": {
    "sealdice": {
      "url": "https://你的骰子地址/sd-api/v2/mcp",
      "headers": { "Authorization": "Bearer <WebUI token>" }
    }
  }
}
```

这是常见客户端配置形式；字段名以客户端自身的 Streamable HTTP 配置为准。
也支持 `Token` 请求头；不接受 URL query token。远程连接应通过 HTTPS。
每个请求（包括 initialize、tools/list、tools/call）重新验证 token，因此 WebUI 密码变更撤销会话后，该 agent 也立即失去访问权限。
此 token 授予与 WebUI 相同的管理能力，包含 JS 插件和连接管理，适合只交给受信任的 agent。

## 使用

客户端先 `initialize`，再 `tools/list` 获取当前版本实际注册的工具及 JSON Schema。
工具名由 HTTP 方法与 API 路径生成，例如：

- `get_base_setting_value`：读取基础设置。
- `put_base_setting_value`：更新基础设置，支持部分字段。
- `get_config_reply` / `put_config_reply`：读取/修改自定义回复总开关。
- `get_config_advanced` / `put_config_advanced`：高级设置。

`tools/call` 参数按位置分组：`path` 是路径参数，`query` 是查询参数，`body` 是原始 API JSON 请求体，`dryRun` 控制是否执行。
所有参数均根据 v2 OpenAPI 的类型、必填字段、枚举、范围等进行校验；不接受拼错的外层参数和请求体中未声明的字段（API 自身声明自由 map 的位置除外）。

读取：

```json
{"name":"get_base_setting_value","arguments":{}}
```

预检修改：

```json
{"name":"put_base_setting_value","arguments":{"body":{"trustOnlyMode":true},"dryRun":true}}
```

实际修改：

```json
{"name":"put_base_setting_value","arguments":{"body":{"trustOnlyMode":true},"dryRun":false}}
```

**GET 默认执行；其他方法默认 dry-run，必须显式传 `dryRun:false` 才会执行。**
预检只做 schema 和路径段校验，返回方法、路径和 `executed:false`，不会调用后端 handler，也不会回显可能包含密码的 body。
预检不保证运行时业务条件满足，例如账号是否在线、文件是否存在、设置之间是否冲突；实际执行仍由原 API 完成这些业务校验。
工具返回原 JSON 响应；HTTP 错误转换为 MCP `isError:true`，不会自动重试写请求。

## 范围和限制

工具从启动时已注册的 v2 OpenAPI 自动生成，执行通过同一个内存 HTTP 路由器，复用 WebUI 的 handler、鉴权、展示模式保护及持久化。
没有任意 URL、任意 API 路径或外部网络代理工具。

覆盖模块：基础设置、config、群组、备份、黑名单、自定义文案、自定义回复、牌堆、日志、JS、帮助、违禁词、连接、扩展。
仅暴露 JSON 请求/响应与简单字符串/数字/布尔 path/query 参数。文件上传/下载、multipart、流式响应、自定义 header/cookie 参数、复杂 query 序列化不暴露；登录/bootstrap、OAuth 绑定、性能分析、实时 WebSocket 和资源上传不暴露。
具体可用操作以 `tools/list` 为准，保持与当前后端 API 同步，不提供另一套设置存储。
请求上限 2 MiB，响应上限 4 MiB，超大查询请收窄条件或使用 WebUI。
不维护 MCP 会话，工具变更随服务重启生效。
