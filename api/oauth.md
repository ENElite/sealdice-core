# WebUI OAuth 登录

支持一个标准 OAuth 2.0 Authorization Code 源，使用 PKCE S256，userinfo 必须返回稳定且非空的字符串 `sub`；token 端点须接受 `client_secret_post`，返回 `access_token` 和 `token_type: Bearer`。源的 authorization/token/userinfo URL 均显式配置，不需要发现服务、JWT 解析或第二套会话系统。

1. 使用现有密码登录新版 WebUI，进入「综合设置 → 基本设置」的 WebUI 密码字段。先设置并保存密码。
2. 展开同位置的「OAuth 登录」，填写 authorization/token/userinfo URL、client ID/secret、scope 和回调 URL。回调 URL 必须是本服务对外地址的 `/sd-api/oauth/callback`，并在第三方 OAuth 客户端登记完全相同的 URL。
3. 保存源，然后点击「绑定第三方账号」。只有这次按钮发起、同一浏览器完成的第三方登录会绑定；回调时管理员会话仍须有效。修改源会清除绑定，保存配置与绑定不会自动相互替代。
4. 以后在 WebUI 解锁框选择「OAuth 登录」。只有已绑定的 `sub` 可以进入；未绑定账号、第三方失败、过期或重复回调均拒绝。登录后写入与密码登录相同的 WebUI token，刷新页面会继续使用现有登录态。
5. 可在设置中解除绑定。密码登录始终保留。

HTTPS 为生产要求；本机标准源测试可使用 `http://localhost` / `http://127.0.0.1` / `http://[::1]`。反向代理须使浏览器、配置的回调 URL 使用相同的外部 origin，并转发 `/sd-api/oauth/*`。新版 UI 入口为 `/v2ui/`；仓库内旧 UI 为外部构建产物，OAuth 操作使用新版 UI。

配置与绑定保存在 `data/webui-oauth.json`，写入采用临时文件原子替换，权限为 `0600`。client secret 从配置读取接口始终剔除，UI 留空保持原 secret；备份部署时需保留该文件。读取失败写入服务日志且不启用 OAuth。配置保存失败不会改变内存配置或绑定。

事务使用 256 位随机 state、独立浏览器 HttpOnly/SameSite=Lax cookie、5 分钟过期和一次性消费。配置更改/解绑会清除在途事务。绑定回调再次验证发起者的 WebUI token，防止登出/密码变更后的绑定；登录回调只接受已绑定 sub。OAuth 提供者的 access token 只用于服务端 userinfo 请求，不返回浏览器。成功登录通过带 nonce CSP 的同源页面存储现有 WebUI token，不把 token 放入 URL，且响应禁缓存、禁止 referrer。

接口边界为 `/sd-api/oauth`：公开 `GET /status`、`POST /login`、`GET /callback`；其余 `GET/PUT /config`、`POST /bind`、`DELETE /binding` 均要求现有管理 token。启动跳转采用 JSON POST（前端随后导航返回的 URL），跨 origin 请求拒绝。展示模式禁止配置、绑定与解绑。

验证：`go test ./api -run TestOAuth -count=1` 使用 `httptest` 标准 OAuth 源，覆盖授权跳转、PKCE、绑定/登录成功、未绑定身份、第三方拒绝/兑换失败、错误浏览器、一次性重放、过期、会话撤销、配置变更失效和 URL 参数校验。前端 HTTP 边界测试为 `pnpm test src/features/auth/oauth.test.ts`。
