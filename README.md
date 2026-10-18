# Solian Go 后端

这是 Solian / Solsynth 已有 Go 后端的续建工作树。主程序是 [`cmd/backend`](cmd/backend)，将身份、账户、聊天、内容、钱包等业务组合为一个进程；原有客户端、HTTP 路径、JSON 契约和数据库协议保持各自的明确边界。

本轮依据 [HOP：面向人类编程](https://github.com/kernel4632/HOP) 整理接手入口、启动流程和业务路由。先看本文定位，再读 [开发与验证](docs/development.md)；自动化协作者同时遵循 [AGENTS.md](AGENTS.md)。

## 从需求找到代码

| 要改什么 | 从哪里读 | 业务与数据 |
| --- | --- | --- |
| 程序启动、参数、外部服务配置 | [`cmd/backend/main.go`](cmd/backend/main.go) | [`options.go`](cmd/backend/options.go) 定义参数，[`configuration.go`](cmd/backend/configuration.go) 校验配置，[`server.go`](cmd/backend/server.go) 运行服务；地址、DSN、公钥分别在 `address.go`、`database.go`、`public_key.go`，领域配置在 `*_config.go` |
| HTTP 路径、服务前缀、路由优先级 | [`internal/application/router.go`](internal/application/router.go) | `route_account.go`、`route_content.go` 等按业务分派；`route_gateway.go` 维护前缀优先级，请求认证在 [`identity.go`](internal/application/identity.go) |
| 业务组合、构造和后台任务 | [`internal/application/backend.go`](internal/application/backend.go) | `account_authentication.go`、`post_publishing.go`、`chat_rooms.go` 等组装业务；`posts.go`、`wallet.go`、`notifications.go` 等组合跨业务操作 |
| 登录、会话、OAuth、权限 | [`ports/authority`](ports/authority) | `internal/authoritypg`、`internal/signinpg`、`internal/oauthpg` |
| 账户、好友、空间、成员、成长 | [`ports/passport`](ports/passport) | `internal/accountpg`、`internal/socialpg`、`internal/realmmemberpg`、`internal/progressionpg` |
| 帖子、时间线、互动、分类、直播 | [`ports/sphere`](ports/sphere) | `internal/postpg`、`internal/timelinepg`、`internal/interactionpg`、`internal/taxonomypg`、`internal/livestreampg` |
| 聊天、房间、同步、通话 | [`ports/messager`](ports/messager) | `internal/chatpg`、`internal/messagerpg`、`internal/realtime`、`internal/callpg` |
| 钱包、资金、订单、商家 | [`ports/wallet`](ports/wallet) | `internal/walletpg`、`internal/commercepg`、`internal/merchantpg` |
| 媒体、通知、应用、联邦 | [`ports/media`](ports/media)、[`ports/notifications`](ports/notifications)、[`ports/develop`](ports/develop)、[`ports/federation`](ports/federation) | 对应 `internal/mediapg`、`notificationpg`、`apppg`、`botpg`、`federationpg` 及网络提供方 |
| 数据结构、就绪检查、数据库用例 | 各业务 `internal/*pg/*.sql`、[`readiness.go`](internal/application/readiness.go) | [`tests/integration`](tests/integration)、[`scripts/integration-postgres.sh`](scripts/integration-postgres.sh) |

典型执行路径：`cmd/backend` → `internal/application` 选择业务与校验身份 → `ports/<业务>` 执行契约和业务规则 → `internal/<业务>pg` 事务读写 PostgreSQL → HTTP 响应或持久投递任务。这里的包边界承担契约、授权和事务职责；整理代码时保留这些职责。

## 先在本地检查

使用 Go **1.26.4**、Python 3、C 编译器（供 race 检查），并预先取得 `go.mod` / `go.sum` 锁定的模块和 Python 依赖。`GO_BIN` 可指向已有 Go 编译器：

```bash
export GO_BIN="${GO_BIN:-go}"
"$GO_BIN" version
"$GO_BIN" mod download
"$GO_BIN" mod verify
python3 -m venv .venv
.venv/bin/python -m pip install -r scripts/sql-parser-requirements.txt
PATH="$PWD/.venv/bin:$PATH" bash scripts/verify.sh
```

`verify.sh` 离线执行证据、SQL、路由相关编译检查，以及 build、vet、普通测试和 race。没有受控的 `PG_TEST_DSN` 时，真实 PostgreSQL 用例会跳过；测试包的 `ok` 不代表数据库、进程重启或现有客户端验收通过。

查看启动参数：

```bash
"$GO_BIN" run -buildvcs=false ./cmd/backend --help
```

实际配置校验和启动示例见 [开发与验证](docs/development.md#配置与启动)。`cmd/backend` 默认只校验配置；显式添加 `--serve` 才连接数据库并监听 `127.0.0.1:8090`。启动不会安装数据库结构、创建用户、发放权限或生成持久密钥。

## 验证与历史依据

本次重构与实际检查结果见 [`docs/hop-refactor-20261018.md`](docs/hop-refactor-20261018.md)，包括完整本地验证日志与源码指纹。

后续真实后端进程与 PostgreSQL 重启验证已通过，结果与证据见 [`docs/backend-e2e-20261018.md`](docs/backend-e2e-20261018.md)。

当前业务范围索引为 [`docs/all-drafts-completion-scope.json`](docs/all-drafts-completion-scope.json)，原始来源闭环为 [`docs/all-drafts-source-closure.json`](docs/all-drafts-source-closure.json)。这些文档中的提交号和测试结果只对应所记录的版本；本次改动的结果以本次验证记录为准。外部支付渠道、迁移部署、旧版本兼容保持原有范围约定。

真实数据库与重启用例默认使用受保护的私有 CI；另有显式本地临时模式供后端进程端到端验证，命令和隔离条件见 [开发与验证](docs/development.md#数据库和重启验证)。本轮不触发 GitHub Actions：用户此前说明月度运行额度已耗尽。不得伪造 CI 环境或撤销测试保护来获得通过标记。外部服务和 NFC 硬件需独立配置与验证，普通测试不构成生产验收。

旧 README 已原样归档到 [`docs/history/README-before-hop-20261018.md`](docs/history/README-before-hop-20261018.md)。`docs/` 下的阶段性设计、审计和 checkpoint 保留为有版本边界的历史证据；优先用当前源码确认运行行为。

本仓库及派生实现遵循 [AGPL-3.0-only](LICENSE.txt)，附加来源与声明见 [NOTICE.md](NOTICE.md)、各包 `PROVENANCE.md` 和源码清单。保留上游快照、许可、oracle 与哈希记录，修改业务实现时不要顺手重写这些证据。
