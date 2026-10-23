# Admin 镜像

在仓库根目录执行 `docker build -t sealdice-admin .`。构建不需要本地 Go、Node、Python、已编译的核心或 UI：镜像内依次生成 OpenAPI、冻结安装 pnpm 依赖、生成客户端并构建嵌入式 UI，最后编译 CGO 核心。BuildKit 缓存 Go 模块、编译结果和 pnpm store。

可通过 `--build-arg GOPROXY=https://goproxy.cn,direct` 指定构建期 Go 代理。私有本机缓存也可使用容器能访问的 HTTP URL；默认使用官方代理。UI 构建会从现有 `go generate` 所配置的发布地址下载旧版 UI。

运行时包含 Python 3.13.5（含 pip/venv）、Bash、Git、Node 22 和固定版本的官方 Codex CLI 0.157.1。生图 Python 依赖由 `imagegen-requirements.lock.txt` 固定，安装在 `/opt/codex-imagegen-venv`。镜像不包含生图业务源码或 Codex 凭据。

将既有 Codex 目录挂载到 `/codex`，`CODEX_HOME` 默认为既有布局 `/codex/home`。如果挂载中存在 `/codex/mcp-imagegen/start-http.sh`，入口脚本会用 Bash 在后台启动它；随后用 `exec` 启动海豹。没有此文件时只启动海豹。`start-http.sh` 应使用 `/opt/codex-imagegen-venv/bin/python` 并使用绝对路径定位业务源码。现有 `/codex/bin/codex` wrapper 可继续调用 `/usr/local/bin/codex`。

持久目录为 `/sealdice/data`、`/sealdice/backups`、`/codex`，海豹端口为 3211。构建上下文排除了这些目录、依赖安装目录、环境配置及认证文件；请继续将服务配置与凭据保存在运行时挂载中。
