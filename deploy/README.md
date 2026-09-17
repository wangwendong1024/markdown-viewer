# Docker 部署

## 当前版本：登录鉴权

服务现在要求登录，使用 SQLite 3.53.4 保存用户密码哈希和会话，默认 Token 8 小时。
配置、首次账号初始化、重置密码、数据库卷和生命周期见 [AUTH.md](AUTH.md)。
健康检查改为无敏感数据的 `/healthz`；`/files`、正文和图片接口必须带登录 Cookie。
以下 2026-09-17 初次部署记录保留为历史证据，不代表当前接口仍匿名开放。

## 标准构建与启动

在项目根目录创建 `.env`（不要提交），指向宿主机上已经存在的文档目录：

```dotenv
MARKDOWN_PORT=18088
MARKDOWN_DOCS_DIR=/absolute/host/documents
```

```sh
docker compose build
docker compose up -d
docker compose ps
```

镜像内运行端口为 3000，宿主机默认监听 `0.0.0.0:18088`。
~~文档目录以只读 bind mount 挂载到 `/docs`。~~
文档目录现以可写 bind mount 挂载到 `/docs`，支持登录后的上传覆盖；历史快照保存在
`/data/document-history`，详情见 [SYNC.md](SYNC.md)。程序仍以非 root 用户运行。
目录须可遍历、可创建和替换文件，现有文件须可读。~~当前服务没有登录认证~~。当前版本强制后端登录认证，
仍适用于受信任局域网；HTTP 不提供传输加密，HTTPS 时启用 Secure Cookie。

## Hackintosh 实例

- 项目目录：`/Users/hackintosh/markdown-viewer`
- 宿主机文档目录：`/Users/hackintosh/markdown-viewer-docs`
- 容器内文档目录：`/docs`
- 浏览地址：`http://192.168.22.59:18088`
- Docker 命令：`/usr/local/bin/docker`
- Compose 服务：`markdown-viewer`

本次按用户要求复制当前文档及其引用图片，不配置 Windows 自动同步。
~~后续更新宿主机文档目录即可由容器读取。~~ 后续通过 `/documents` 或同步接口更新，
才能保留历史；直接写宿主机绕过版本管理。无需重建镜像。不要把 Windows 的
`C:` 路径直接作为 Mac Docker 的挂载源。

生产构建使用相同站点的 `/files`、`/file`、`/stat` 请求，避免非 3000 端口
误连到访问者本机的 `127.0.0.1:3000`；~~开发服务器原有行为保留~~。
当前开发服务器也使用同源 API，由 Webpack 转发到 Go 后端。

## 镜像仓库不可达时的构建

当 Docker 虚拟机不能下载镜像/依赖，但构建主机可联网时，可用已有的
Node/Yarn 和 Go 编译 Linux amd64 程序，然后用已有 Alpine 镜像打包。
在干净的构建目录中执行；脚本拒绝覆盖已有 `bindata.go`。

**下面 Go 1.23 命令仅保留为无认证旧版本的历史记录，不适用于当前登录版本。**
当前要求 Go 1.26；Catalina 不支持其 macOS 二进制，应在 Windows/Linux 编译后，
上传 Linux 程序到 `dist/docker/markdown-viewer`，使用 `deploy/Dockerfile.prebuilt` 构建镜像。

```sh
cd /Users/hackintosh/markdown-viewer
export PATH=/Users/hackintosh/.nvm/versions/node/v22.23.2/bin:$PATH
export GO_BIN=/Users/hackintosh/sdk/go1.23.0/bin/go
export DOCKER_BIN=/usr/local/bin/docker
sh deploy/build-prebuilt.sh
/usr/local/bin/docker compose up -d --no-build
```

~~标准 Dockerfile 使用 Node 22、Go 1.24 和 Alpine 3.22~~。
当前标准构建为 Node 22、Go 1.26 和 Alpine 3.22；替代构建使用指定的
主机 Go 版本。前端依赖按 `yarn.lock` 冻结安装，Go 依赖由 `go.sum` 校验。
旧版 Webpack 的哈希算法需要在 Node 22 构建时启用 OpenSSL legacy provider；
脚本仅对前端构建命令设置此选项，运行容器不使用 Node。

## 验证与停止

```sh
cd /Users/hackintosh/markdown-viewer
/usr/local/bin/docker compose ps
/usr/local/bin/docker compose logs --tail=30
curl -fsS http://127.0.0.1:18088/healthz
```

还需要从局域网另一台电脑访问 `http://192.168.22.59:18088`，确认正文、目录和
图片实际加载。`/files` 可用或容器启动不等于浏览器验收通过。

```sh
/usr/local/bin/docker compose down
```

停止并移除本服务容器不删除宿主机文档目录，也不操作其他服务。
`restart: unless-stopped` 依赖 Docker Desktop 自身已经启动，不代表 Mac 重启后
无需登录即可访问；本次不修改系统启动或 Docker 全局代理配置。

## 2026-09-17 部署验收

基础代码为 `mymakrdown` 分支 `8ec2bc7`，加本次未提交的部署配置及同源请求修复。
实际采用主机预编译方式，构建退出码 0；运行镜像 ID：
`sha256:c105663b98a94678ccd96d8760a2c003943c0ea35986e675fdfd176701fa7aa1`。

| 编号 | 用户要求 | 实测证据 | 状态 |
| --- | --- | --- | --- |
| D01 | 在 Hackintosh 用 Docker 运行 | Compose 容器 running，健康检查 healthy | 一致 |
| D02 | 对外映射端口，局域网可访问 | `0.0.0.0:18088 -> 3000`；Windows HTTP 与浏览器访问成功 | 一致 |
| D03 | 指定文档复制到 Mac 磁盘 | Windows、Mac、容器内文档 SHA-256 相同；147676 字节 | 一致 |
| D04 | 容器从映射路径读取 | inspect 显示宿主机目录 bind 到 `/docs`，`RW=false`；`/files` 返回该文档 | 一致 |
| D05 | 只复制当前版本 | 无同步任务；正文与引用 PNG 已复制，浏览器正文及 2200 像素宽图片加载成功 | 一致 |

应检查 5 项 = 一致 5 + 不匹配 0 + 缺失 0 + 无法确认 0；额外项 0。
同源请求回归检查覆盖生产映射端口、HTTPS 端口及原开发端口，共 4 项通过。
新文件 UTF-8 无 BOM / LF 检查和 `git diff --check` 通过。

只部署请求的 Markdown 及显示所需 PNG；文内指向其他源码、CSV、附录或 SVG
的链接不属于本次复制范围，不能据正文加载成功认定那些目标也已部署。
标准多阶段 Docker 构建未完成验证，原因是 Docker 虚拟机出网/镜像代理失败；
实际交付的预编译镜像已完成上述运行验收。
