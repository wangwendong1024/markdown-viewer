# 文档同步与历史版本

## 使用

登录后进入 `/documents`（阅读器右上角“文档管理”）。选择 UTF-8 `.md` 文件，默认
以原文件名同步到文档根目录；可指定已有子目录，如 `notes/方案.md`。同一相对路径
为同一文档；不同目录同名文件互不覆盖。大小写必须与现有文件完全一致。

新文件直接写入；同路径内容变化时，先保存旧内容，再替换当前内容；字节完全相同则跳过。
历史列表按保存时间倒序排列，可查看原文、下载或恢复。恢复前会保存当前内容。
历史版本不自动清理，不提供删除接口；首次纳入管理之前的历史无法补回。
仅 Markdown 正文同步，图片/附件保留现有文件，不会自动打包上传。

## 接口

所有接口沿用登录 Cookie、8 小时生命周期和来源校验；未登录 401、跨源 403。
同一认证账号可读写所有文档，暂不提供文档级权限。

| 方法与路径 | 参数/请求 | 结果 |
| --- | --- | --- |
| PUT `/api/documents/sync?path=<相对路径>` | UTF-8 Markdown 原始请求体，最大 8 MiB | path、sha256、changed、previous_version |
| GET `/api/documents/history?path=...` | 同步路径 | versions 数组；id、saved_at、saved_by、sha256、size |
| GET `/api/documents/version?path=...&version=...` | 历史 ID；可加 download=1 | text/plain 原始内容；下载为 version.md |
| POST `/api/documents/restore?path=...&version=...` | 无请求体 | 与 sync 相同的结果 |

禁止绝对路径、父目录跳转、符号链接、隐藏路径、非 `.md` 目标、NUL 与无效 UTF-8。
子目录须已存在。使用 Go `os.Root` 限制文件访问边界；历史版本读取时验证路径、版本号、
内容长度及 SHA-256。接口不接受任意宿主机路径。

## 存储与部署

- 当前文档：原有 Hackintosh 文档 bind mount，容器 `/docs`，现改为可写。
- 历史：`AUTH_DB_PATH` 所在目录下的 `document-history/<路径SHA256>/<版本ID>.json`。
  默认位于 `/data/document-history`，持久化在既有 `markdown-viewer_auth-data` 卷中。
- 快照 JSON 包含内容的 base64 编码、UTC RFC3339Nano 时间、触发保存的账号和校验摘要。
  历史放在阅读目录之外，只能通过登录接口获取。没有新增 SQLite 表或外键。
- 历史写入失败时不覆盖当前文件。快照及新文件均先写临时文件、Sync，再 Rename。
  若快照成功后替换失败，可能多出一份快照，可安全重试。
- 限单个应用进程/单副本写入同一文档与历史卷；进程内互斥串行化更新。
  不支持外部编辑器同时写入；直接 SCP/宿主机修改绕过历史保护。
- 备份需同时覆盖当前文档目录和 `/data`。磁盘满时同步失败，不自动删旧历史腾空间。
  进程重启后的持久化已验证；宿主机掉电耐久性、备份恢复演练和大量版本性能未验证。

构建新增嵌入资源 `ui/documents.html`，标准 Dockerfile 已包含该资源；预编译流程不变。
更新 Compose 后重新创建容器以启用可写文档挂载。保留旧认证镜像和旧 Compose 可回退到
只读浏览，现有历史留在 `/data` 不删除。不要执行 `down -v`。

## 2026-09-17 验证

Windows 与 Linux amd64 容器均通过 11 项 Go 测试（原认证 8 项 + 文档 3 项）。
新增测试覆盖：覆盖/恢复/重复跳过、快照写失败保留原文、路径与大小写/符号链接限制、
损坏历史检测、请求大小与编码、匿名/跨源拒绝、6 次并发写入全部内容可追溯。
真实 Edge 浏览器验证 UI 上传、覆盖、重复跳过、预览、下载、恢复；390px 手机宽度无横向溢出。
生产端口仍为 `18088`，镜像为 `markdown-viewer:versions-20260917`，管理入口 `/documents`。
保留“同步与历史版本示例.md”用于展示，不修改既有业务文档。
容器重启后，同一示例内容同步返回 unchanged；通过全局 skill 脚本再次修改返回
replaced_with_history，读回当前正文与历史快照均通过 SHA-256 校验。
上线镜像 ID：`sha256:25a706570a82100b4a2cdc9cadbaf4d776a609962ec59a3cf31af95d2c65c3ec`。
旧镜像保留为 `markdown-viewer:before-versions-20260917`，旧 Compose 保存为
`compose.before-versions-20260917.yaml`。
