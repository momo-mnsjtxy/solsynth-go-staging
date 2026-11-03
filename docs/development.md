# 开发指南

本文帮助你从一个业务需求找到实现、修改代码并运行检查。编译和启动命令在 [README](../README.md)，参数解释在 [配置说明](configuration.md)。下文命令都在仓库根目录执行。

## 从需求找到代码

| 要改什么 | 先读哪里 | 业务与持久化 |
| --- | --- | --- |
| 启动、参数、关闭流程 | [cmd/backend/main.go](../cmd/backend/main.go) | `options.go` 登记参数，`configuration.go` 校验配置，`server.go` 管理服务生命周期 |
| HTTP 路径、服务前缀和别名 | [internal/application/router.go](../internal/application/router.go) | 相邻 `route_*.go` 按业务分派，`route_gateway.go` 决定前缀优先级，`route_stargate.go` 对应 Stargate 迁移路径 |
| 登录、会话、OAuth、权限 | [ports/authority](../ports/authority) | `internal/authoritypg`、`internal/signinpg`、`internal/oauthpg` |
| 账户、好友、空间、成员、成长 | [ports/passport](../ports/passport) | `internal/accountpg`、`internal/socialpg`、`internal/realmmemberpg`、`internal/progressionpg` |
| 帖子、时间线、互动、分类、直播 | [ports/sphere](../ports/sphere) | `internal/postpg`、`internal/timelinepg`、`internal/interactionpg`、`internal/taxonomypg`、`internal/livestreampg` |
| 聊天、房间、同步、通话 | [ports/messager](../ports/messager) | `internal/chatpg`、`internal/messagerpg`、`internal/realtime`、`internal/callpg` |
| 钱包、资金、订单、商家 | [ports/wallet](../ports/wallet) | `internal/walletpg`、`internal/commercepg`、`internal/merchantpg` |
| 媒体、通知、应用、联邦 | [ports/media](../ports/media)、[ports/notifications](../ports/notifications)、[ports/develop](../ports/develop)、[ports/federation](../ports/federation) | 对应 `internal/mediapg`、`notificationpg`、`apppg`、`botpg`、`federationpg` 及提供方实现 |
| 表结构、就绪检查、数据库测试 | 各业务 `internal/*pg/*.sql` | `internal/application/readiness.go`、`tests/integration`、`scripts/integration-postgres.sh` |

这里的 `ports` 包定义业务操作和 HTTP 契约，`internal/*pg` 实现 PostgreSQL 读写与事务。`internal/application/backend.go` 的 `NewPostgres` 把它们组合起来；`identity.go` 建立请求身份，`transaction.go` 处理跨业务事务。

## 顺着一个功能阅读

以发帖为例，按调用顺序读：`cmd/backend` → `internal/application/route_content.go` → `ports/sphere/posts` → `internal/postpg`。组装逻辑在 `post_publishing.go`，跨业务操作在 `posts.go`。先看 HTTP 输入和响应，再跟进业务规则、权限检查和事务写入。

查其他业务的组合关系时，从 `internal/application` 中相应文件开始：

| 业务 | 组装文件 |
| --- | --- |
| 账户、登录、成长、日历 | `account_authentication.go`、`account_lifecycle.go`、`account_calendar.go` |
| 发布、阅读、翻译、问卷、贴纸 | `post_publishing.go`、`reading_content.go`、`content_engagement.go` |
| 聊天房间、聊天媒体、机器人投递 | `chat_rooms.go`、`bot_delivery.go` |
| 开发者应用、商家、订单 | `developer_commerce.go` |
| 媒体存储、通知、实时投递 | `media_storage.go`、`notification_delivery.go` |

修改前搜索调用方和现有测试，例如：

```bash
rg -n 'CreatePost|/api/posts' cmd internal ports tests
```

同包的 `*_test.go` 主要覆盖逻辑和 HTTP 契约；真实 PostgreSQL、事务竞争和重启用例在 `tests/integration`。修改涉及授权、通知或多个业务写入时，一并阅读 guard、事件投递和锁顺序，避免只改表面的 HTTP 处理。

沿用现有 Go 包和业务命名。文件头说明职责与调用方法，复杂步骤旁写清顺序和原因。Go 自动编译同包源码；新 HTTP 路由、数据库 DDL 和集成用例仍需在各自入口显式登记，防止无意暴露接口或执行迁移。

## 工具和日常检查

准备 Go **1.26.4**、Python 3 和 C 编译器。Go 模块按 README 下载；Python 依赖固定在 `scripts/sql-parser-requirements.txt`。下面用 Python **3.12** 创建隔离环境，放进被 Git 忽略的本地缓存目录：

```bash
export GO_BIN="${GO_BIN:-go}"
python3.12 -m venv .cache/verify-venv
.cache/verify-venv/bin/python -m pip install -r scripts/sql-parser-requirements.txt
```

SQL 检查固定使用 PostgreSQL 16 语法。如果软件源找不到 `pglast==6.7`，先用 Git 和 C 编译器从上游同版本构建，再重跑上面的依赖安装命令：

```bash
.cache/verify-venv/bin/python -m pip install 'git+https://github.com/lelit/pglast.git@v6.7'
```

先检查改动涉及的包，再运行统一检查。下面以启动、路由和三个常见业务为例，按实际改动选择包：

```bash
"$GO_BIN" test -count=1 ./cmd/backend ./internal/application
"$GO_BIN" test -race -count=1 ./ports/sphere/posts ./ports/messager/rooms ./ports/wallet/accounts
PATH="$PWD/.cache/verify-venv/bin:$PATH" bash scripts/verify.sh
```

`verify.sh` 离线执行来源、SQL、路由及集成用例登记、fixture 隔离和格式检查，然后运行 build、vet、普通测试和 race。依赖必须提前下载；它不会在检查时联网补模块。未配置受控的 `PG_TEST_DSN` 时，真实数据库用例会跳过。

## 配置与启动

使用 [README 的启动示例](../README.md#配置与启动)。修改启动参数时，参数登记从 `cmd/backend/options.go` 找，业务校验从相应 `*_config.go` 找；数据库 URL、公钥和监听地址分别由 `database.go`、`public_key.go`、`address.go` 校验。

## 数据库和重启验证

本地集成测试使用运行器创建的临时 PostgreSQL 16 集群。先准备 PostgreSQL 工具、Go 和上述 Python 环境，并将待测修改提交到干净的 Git HEAD；运行器用该提交绑定测试源码。`PG_TEST_ROOT` 必须是绝对路径。不要传入现有数据库的 DSN。

后端进程与重启测试：

```bash
PATH="$PWD/.cache/verify-venv/bin:$PATH" \
  PG_BIN=/path/to/postgresql16/bin GO_BIN=/path/to/go1.26.4/bin/go \
  PG_TEST_ROOT=/tmp/solian-e2e \
  bash scripts/integration-postgres.sh --local-disposable --backend-e2e
```

这会运行 `TestPGBackendHTTPProcessRestart`、`TestPGBackendSigninHTTPProcessRestart`、`TestPGBackendRestartPersistence`，停止并重启 PostgreSQL 后再检查恢复。它验证后端 HTTP/WebSocket 和持久化链路，不包含 Solian 前端。

若环境禁止 Unix socket，但允许本机 TCP，在命令末尾加 `--local-tcp`。它只支持上述后端 E2E 子集，使用随机本机端口和临时 SCRAM 密码，保留集群标记、源码检查及空模板检查。本地模式拒绝混入 `CI`、`GITHUB_*`、`PG_TEST_PRIVATE_CI`。

完整数据库套件分为两个分组，各自需要重启后检查，再由结果脚本汇总。分组定义和调用方式见 `scripts/integration-groups.json`、`scripts/integration-postgres.sh`、`scripts/check-integration-results.py`；原有私有 CI 入口保留。后端 E2E 子集通过不能替代完整数据库套件通过。

当前约定不触发 GitHub Actions。需要提交时使用带 `[skip ci]` 的说明，推送前检查工作流触发条件，不修改工作流来省运行额度。

## 来源、文档与变更边界

`source/`、`upstream/`、`inventory/`、`*-evidence/`、manifest、`PROVENANCE.md` 和独立 oracle 保存固定来源或历史结果。`scripts/check-source-evidence.py` 会检查这些内容；失败时找实际差异，不刷新哈希。日常业务修改默认保持 HTTP 契约、身份验证、事件顺序、数据库结构与锁协议。

文档更新方式见 [文档目录](README.md#维护文档)，旧设计和已有测试记录从 [历史文档](history/README.md) 查。
