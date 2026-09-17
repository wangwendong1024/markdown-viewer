# 登录、SQLite 与 Token 生命周期

## 运行合同

- SQLite **3.53.4**，通过 `modernc.org/sqlite v1.59.0` 嵌入 Go 程序，不使用宿主机旧 SQLite。
- Go 1.26.0；密码使用 bcrypt cost 12，每条密码自动生成随机盐，数据库不存明文密码。
- 用户名为 3–64 个 ASCII 字母、数字、点、下划线或短横线，后端统一转小写。
- 密码为 12–72 字节；中文密码按 UTF-8 字节计算。
- 不开放注册；管理员通过容器命令初始化账号。全部账号具有已挂载文档的阅读权限。
- Token 是 32 个密码学随机字节的 base64url 编码。仅通过 HttpOnly、SameSite=Strict Cookie
  发送给浏览器，数据库仅保存 SHA-256 摘要；前端不使用 localStorage 存 Token。
- 固定有效期默认 8 小时，不滑动续期、不无限刷新。后端每次读取 SQLite 校验过期时间和账号状态。
- 同一浏览器重新登录撤销旧 Token；退出删除当前会话；重置密码撤销该用户全部会话。
- 其他设备的独立会话可以并存。到期后接口返回 401，页面返回登录页。
- 重启容器保留未过期会话；挂载文档与认证数据库使用不同目录。

来源：[SQLite 稳定版](https://sqlite.org/releaselog/3_53_4.html)、
[Go 驱动](https://pkg.go.dev/modernc.org/sqlite@v1.59.0)。

## 后端接口

| 方法 / 路径 | 行为 |
| --- | --- |
| GET `/login` | 登录 HTML，不返回任何文档内容 |
| POST `/api/auth/login` | JSON `username,password`；成功设置 Cookie，返回 `username,expires_at,expires_in` |
| GET `/api/auth/me` | 当前账号、UTC 到期时刻；需要 Cookie |
| POST `/api/auth/logout` | 撤销当前会话并清理 Cookie；重复退出幂等 |
| GET `/healthz` | 数据库连通性；不暴露用户、文件或 Token |
| `/files`、`/file`、`/stat`、`/forward` | 均经过后端鉴权，未登录返回 401 |
| 文档页面 | 未登录 303 跳转到 `/login`，保留原路径用于登录后返回 |

登录请求最大 2 KiB，同一来源 IP 五分钟内最多 10 次尝试（含成功），最多同时进行
4 次 bcrypt 验证，超出返回 429。客户端传来的 X-Forwarded-For 不用于绕过限流。
无效用户名与错误密码统一报错；拒绝跨源浏览器请求，取消原 API 的通配 CORS。
开发服务器通过 Webpack 同源代理访问 Go 后端。

## 配置与持久化

| 配置 | 默认 / 限制 |
| --- | --- |
| `AUTH_DB_PATH` | Docker 为 `/data/auth.db`；本地为 `data/auth.db`；必须在文档目录之外 |
| `AUTH_TOKEN_TTL` | `8h`，可设 1 分钟至 720 小时；改变配置只影响新会话 |
| `AUTH_COOKIE_SECURE` | `false` 兼容当前局域网 HTTP；HTTPS 部署必须设为 `true` |

Compose 命名卷 `auth-data` 映射到 `/data`。容器 UID/GID 为 65534，目录 0700、数据库
0600。使用 WAL、5 秒 busy timeout、FULL synchronous 和单连接串行写入。根文件系统
~~和文档挂载只读，数据库卷可写。~~ 根文件系统只读，文档挂载和数据库卷可写，
支持 [带历史版本的文档同步](SYNC.md)。不要执行 `docker compose down -v`，
它会删除账号、会话与历史版本。所有登录账号共享文档的读写和恢复权限。

当前 HTTP 不提供密码或 Cookie 的传输加密，限受信任局域网；HTTPS 反向代理应保留 Host，
并启用 Secure Cookie。本实现不提供 MFA、找回密码邮件或按文档划分权限。

## 账号操作

通过 SSH 进入 Mac 后，在交互式 Bash 运行以下步骤。密码不放进命令行参数、仓库或镜像：

```bash
cd /Users/hackintosh/markdown-viewer
read -r -s -p 'Initial password: ' AUTH_INPUT; printf '\n'
printf '%s' "$AUTH_INPUT" | /usr/local/bin/docker compose exec -T markdown-viewer \
  /usr/local/bin/markdown-viewer --create-user admin
unset AUTH_INPUT
```

重置密码使用同样的 stdin 流程，将 `--create-user` 替换成 `--set-password`。
重置事务同时撤销全部会话；不会删除用户或修改文档。

## 数据库设计与运维

Target database foreign keys: none. Existing database foreign keys found: none.
假设为单节点、小团队（约 100 个账号、最多约 1 万活跃/待清理会话），以会话读取为主。
尚无实际生产峰值 QPS、RPO/RTO 或人审性能目标，不能宣称大规模生产性能已验收。

`auth_schema.sql` 是版本化结构与元数据来源：

| 表 / 关系 | 权威内容、约束、索引及生命周期 |
| --- | --- |
| users | INTEGER 主键；唯一用户名；密码哈希；disabled；创建时刻。CLI 创建，默认长期保留，不提供硬删除 API |
| sessions | Token 摘要主键；user_id 逻辑引用 users.id；创建和过期时刻；按 user_id 与 expires_at_ms 建索引 |
| sessions.user_id → users.id | 后端在同一事务 INSERT SELECT 校验存在、启用及密码哈希未被并发修改；认证查询 JOIN 再校验 |

创建由唯一约束处理重复；密码重置在同一事务更新哈希并删除会话。若未来加入用户删除，
必须在同一写事务先删除会话再删除用户；不允许外部 SQL 绕过该协议。
运维核对孤儿：`SELECT count(*) FROM sessions s LEFT JOIN users u ON u.id=s.user_id WHERE u.id IS NULL;`。
孤儿不会通过认证，出现后应排查外部数据库写入；清理须小批次、保留操作记录。

每次成功登录按过期索引删除最多 1000 个过期会话。无新登录时过期记录可留待下次清理，
但访问权限立即失效。用户名查询走 UNIQUE 索引，Token 校验走主键，用户撤销走 user_id
索引，过期清理走 expires_at_ms 索引。账号很少，额外索引和读写分片没有必要。

SQLite 无原生带时区时间类型。使用 UTC epoch 毫秒作为唯一权威时间，直接满足 Go
UnixMilli 到期比较合同，避免浮点 Julian day 转换和客户端时区解释；不是宣称整数普遍更快。
`users_operations`、`sessions_operations` 暴露毫秒精度 UTC `Z` 与上海 `+08:00` 派生显示，
上海在前；排序和筛选仍使用整数。CHECK 将范围限定为 2020–2100，该区间上海无夏令时，
固定 +08:00 转换不用于历史日期。测试验证两种显示回转同一毫秒，并检查过期查询索引。

首次启动幂等 CREATE TABLE/INDEX/VIEW；不迁移或修改文档数据。备份须停止本服务后复制
完整 `/data`（含可能存在的 WAL/SHM），或使用 SQLite 在线备份 API；不能在写入中只复制
auth.db。恢复到独立卷后验证 integrity_check、用户登录、过期与撤销行为，再替换；备份恢复
演练尚未执行。回退无认证旧镜像会重新公开文档，因此登录上线后故障应优先停止服务。

## 构建

标准构建：`docker compose build` 使用 Go 1.26 与固定 go.mod/go.sum。
旧 Catalina 无法运行 Go 1.26 的 macOS 二进制，可在 Windows 使用 g.exe 切换到 1.26.0
后构建前端与 Linux 程序，再按 `deploy/Dockerfile.prebuilt` 打包。前端仅构建时使用
`NODE_OPTIONS=--openssl-legacy-provider` 兼容旧 Webpack，运行镜像不包含 Node 或编译器。

## 2026-09-17 实测与上线记录

Windows Go 1.26.0 和 Linux amd64 容器均通过 8 个后端测试，覆盖认证、Token 生命周期、
持久化、账号禁用、限流/跨源、SQLite/索引/时间投影、请求边界、并发读取、重置密码撤销。
Linux 上 20 次并发会话查询合计约 13.5ms，仅是本地小样本结果，不外推生产容量。

| ID | 要求 / 检查 | 结果与证据 | 状态 |
| --- | --- | --- | --- |
| A01 | 登录 UI | 浏览器显示登录表单；错误密码提示；成功进入阅读器；退出按钮有效 | 一致 |
| A02 | 最新 SQLite | Windows 与 Linux `SELECT sqlite_version()` 都是 3.53.4 | 一致 |
| A03 | 用户和密码持久化 | SQLite 用户表保存 bcrypt 哈希；容器重启后 admin 登录/未过期会话可用 | 一致 |
| A04 | 后端接口鉴权 | 正式 18088 的文件列表、正文、stat、图片和 me 未登录均返回 401 | 一致 |
| A05 | Token 到期 | 单元测试验证到期毫秒边界；测试容器 1 分钟 Cookie 在浏览器自动跳回登录页 | 一致 |
| A06 | 退出/重置撤销 | 正式退出后接口拒绝访问；测试验证旧密码和所有旧会话在重置后失效 | 一致 |
| A07 | 挂载文档继续可读 | 正式 admin 登录后可读取既有文档与图片，原文档挂载仍只读 | 一致 |
| A08 | 默认生命周期与注册策略 | 正式登录返回 expires_in=28800；无注册入口；首个 admin 经 stdin 命令创建 | 一致 |

8 项 = 一致 8 + 不匹配 0 + 缺失 0 + 无法确认 0；额外项 0。
测试容器与测试数据库卷已移除；测试账号未写入正式数据库。
正式镜像 `sha256:fe8f9117cae3e23c0eaf6685f1c685647bf39cadfcec204c00972f7782a509ad`，
认证卷 `markdown-viewer_auth-data`，端口 `18088:3000`，文档挂载保持原位置。
首次数据库卷由初始化命令预创建，Compose 可能提示非 Compose 创建；同名卷被正确复用，
不要为消除提示删除它。

初始密码只保存在操作者本机受限文件，不在此文档、Git、镜像或 Agent Memory 中记录。
账号可用不等于 HTTPS 已启用；当前仍是局域网 HTTP。完整在线 Docker 多阶段构建未验证，
实际采用 Mac 构建前端、Windows 编译 Linux 后端、Hackintosh 打包 Docker 镜像的方式。
