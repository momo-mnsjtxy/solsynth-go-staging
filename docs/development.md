# 开发与验证

本文面向第一次接手现有 Go 后端的开发者。项目入口和按业务找代码的表格在 [README](../README.md)；历史设计与阶段结果有各自的提交边界，不能替代当前源码和本次执行结果。

## 顺着一个功能阅读

先找到调用者使用的 HTTP 路径，再在 `internal/application/router.go` 和相邻业务路由文件中找到它交给哪个模块。`route_ingress.go` 处理设备、成员、PassKit 和公开协议；`route_account.go`、`route_locations.go` 处理账户与位置别名；`route_gateway.go` 保持服务前缀覆盖顺序；`route_content.go`、`route_social.go`、`route_utilities.go`、`route_commerce.go` 分别处理内容、社交、工具和商业路由。`internal/application/backend.go` 的 `NewPostgres` 组合模块；同目录的 `identity.go` 建立受验证的请求身份，业务组合文件把身份和跨业务依赖接上。

随后读 `ports/<业务>` 下的 HTTP 输入、业务操作和数据契约，最后读实际注入的 `internal/<业务>pg` 数据实现与 SQL。同一目录中的 `*_test.go` 覆盖纯逻辑与 HTTP 契约；`tests/integration` 才包含真实 PostgreSQL、事务竞争和重启用例。不要从历史独立命令误判统一主程序的覆盖面。

`internal/application` 的业务组装可以按以下文件直接查找：

| 业务 | 组装文件 |
| --- | --- |
| 账户、登录、成长、日历 | `account_authentication.go`、`account_lifecycle.go`、`account_calendar.go` |
| 发布、阅读、翻译、问卷、贴纸、引用 | `post_publishing.go`、`reading_content.go`、`content_engagement.go` |
| 房间、聊天媒体、机器人执行 | `chat_rooms.go`、`bot_delivery.go` |
| 开发者应用、商家与订单 | `developer_commerce.go` |
| 媒体存储与保护、通知与实时投递 | `media_storage.go`、`notification_delivery.go` |

例如帖子主线是：`cmd/backend` → `internal/application` 的帖子路由及 `posts.go` → `ports/sphere/posts` → `internal/postpg`。聊天主线落到 `ports/messager/rooms`、`internal/chatpg` 和 `internal/messagerpg`；钱包主线落到 `ports/wallet/accounts` 和 `internal/walletpg`。跨域写入还要读 `internal/application/transaction.go`、业务 guard 和锁顺序。

修改前用 `rg -n '路径片段|操作名' cmd internal ports tests` 检查调用方。按业务命名新文件，将业务决策、特殊顺序和输入检查的理由写在附近。Go 包编译会自动包含目录中的 `.go` 文件；HTTP 路由和数据库 schema 则需要显式、安全地接线，不能通过扫描文件自动暴露接口或安装 DDL。

## 工具和日常检查

验证基准编译器为 Go 1.26.4，模块语言版本由 `go.mod` 指定。Python 3 依赖固定在 `scripts/sql-parser-requirements.txt`，race 检查需要 C 编译器。

```bash
export GO_BIN="${GO_BIN:-go}"
"$GO_BIN" version
"$GO_BIN" mod download
"$GO_BIN" mod verify
python3 -m venv .venv
.venv/bin/python -m pip install -r scripts/sql-parser-requirements.txt
```

完整本地检查使用同一个入口，它会关闭 Go 网络拉取：

```bash
PATH="$PWD/.venv/bin:$PATH" bash scripts/verify.sh
```

需要先验证单个业务时，直接运行对应包；通过后再执行完整检查：

```bash
"$GO_BIN" test -count=1 ./internal/application ./cmd/backend
"$GO_BIN" test -race -count=1 ./ports/sphere/posts ./ports/messager/rooms ./ports/wallet/accounts
```

`scripts/verify.sh` 同时检查工作流 YAML、保留来源、集成测试注册、fixture 隔离、PostgreSQL 16 语法、就绪关系、操作清单、SQL 参数与 `gofmt`。这些检查是现有工程约束；不要因只改路由就跳过来源检查，也不要为通过检查刷新已锁定的证据哈希。

## 配置与启动

`cmd/backend/main.go` 顺序执行管理命令分派、参数解析、配置校验和服务启动。`options.go` 是命令行参数与默认值的唯一来源；`configuration.go` 保持身份、票据、聊天、资金和外部提供方的校验顺序，并负责释放配置阶段取得的资源；`server.go` 管理连接检查、监听、后台任务和关闭。监听地址、显式数据库 URL 和公钥文件的安全边界分别集中在 `address.go`、`database.go`、`public_key.go`。

默认动作是校验显式配置。只有 `--serve` 会打开数据库、检查所需 schema 并监听 HTTP。配置检查阶段可能读取明确指定的密钥、语料、文件或提供方配置；它不连接数据库，也不安装数据库结构。

以下 Bash 示例使用**外部已准备好的** RSA 公钥、显式数据库 URL 和实际数据库的聊天枚举。先把变量填为目标开发环境的值；不要把真实密钥或 DSN 写进仓库。分类公开值固定为当前 Go schema 的 `0`，通知的三个枚举必须各不相同。

```bash
export GO_BIN="${GO_BIN:-go}"
export SOLSYNTH_BACKEND_DSN='postgresql://synthetic@127.0.0.1/solian_dev?sslmode=disable'
export SOLIAN_PUBLIC_KEY_FILE='/absolute/path/to/trusted-public.pem'
export SOLIAN_ISSUER='https://issuer.example.test'
export SOLIAN_AUDIENCE='solian-development'
export SOLIAN_KEY_ID='development-key'
export SOLIAN_CHAT_ENCRYPTION_NONE='0'
export SOLIAN_CHAT_NOTIFY_ALL='0'
export SOLIAN_CHAT_NOTIFY_MENTIONS='1'
export SOLIAN_CHAT_NOTIFY_NONE='2'

backend_args=(
  --issuer "$SOLIAN_ISSUER"
  --audience "$SOLIAN_AUDIENCE"
  --key-id "$SOLIAN_KEY_ID"
  --trusted-public-key-file "$SOLIAN_PUBLIC_KEY_FILE"
  --taxonomy-public-visibility 0
  --messager-none-encryption "$SOLIAN_CHAT_ENCRYPTION_NONE"
  --messager-notify-all "$SOLIAN_CHAT_NOTIFY_ALL"
  --messager-notify-mentions "$SOLIAN_CHAT_NOTIFY_MENTIONS"
  --messager-notify-none "$SOLIAN_CHAT_NOTIFY_NONE"
)
"$GO_BIN" run -buildvcs=false ./cmd/backend "${backend_args[@]}"
```

示例 URL、issuer 和枚举仅展示合成开发配置，不对应上游生产策略。公钥 PEM 必须真实存在且匹配目标 issuer；验证模式不会生成它。TCP DSN 默认要求 `verify-full`，只有明确本地/临时环境支持关闭 TLS。程序拒绝环境中非空的 `PGHOST`、`PGPASSWORD`、`PGSERVICE` 等隐式 libpq 配置；使用独立环境并通过 `--dsn-env` 指定唯一的 DSN 变量。

目标开发库的 schema 与依赖已经由操作者审查并安装后，在同一终端显式启动：

```bash
"$GO_BIN" run -buildvcs=false ./cmd/backend "${backend_args[@]}" --serve --listen 127.0.0.1:8090
```

`/healthz` 用于存活检查，`/readyz` 检查当前启用模块要求的关系和部分完整性约束。`internal/application/readiness.go` 按业务阶段顺序返回首个错误，内部原因保留用于排查，对外继续返回受控状态；它不能代替数据库迁移、完整 schema 审核或客户端验证。启动失败不会自动建表或补用户。

默认后端只配置 token 公钥验证。登录或 OAuth 签发须显式开启相应能力，并提供独立私钥、刷新哈希和 seal 密钥；所有参数以 `go run ./cmd/backend --help` 和 `cmd/backend/*_config.go` 为准。媒体、SMTP、CAPTCHA、推送、通话、联邦、NFC 等提供方也按功能显式启用。缺少配置时保留受控错误，禁止替换成伪造成功。

`cmd/backend` 还提供联邦政策初始化/撤销等显式子命令。它们会写入数据库，不能当配置预检运行；操作依据见 [联邦公开接纳](federation-public-admission.md) 和对应命令源码。

## 数据库和重启验证

`tests/integration` 中的真实数据库测试需要受保护的临时集群、合成数据和明确的候选提交。未配置 `PG_TEST_DSN` 的普通 `go test` 会跳过这些用例。`scripts/integration-postgres.sh` 仅允许现有授权私有 GitHub Actions 环境创建临时数据库；不得在本地伪造 `GITHUB_ACTIONS`、`PG_TEST_PRIVATE_CI` 或撤销 marker/fixture guard。

合格数据库证据必须同时包含当前提交的普通检查、两个 PostgreSQL 分组、各自的数据库重启后断言以及汇总校验，详见 `.github/workflows/`、`scripts/integration-groups.json` 和 `scripts/check-integration-results.py`。进程/数据库重启与纯单元测试是不同验证层次，报告时分别列出。

本轮不触发 Actions，原因是用户此前明确月度运行额度已耗尽。保存提交或 PR 时也检查工作流的 `push` 触发行为；使用跳过 CI 的提交说明，不能为省额度改掉测试保护。普通检查的成功只说明实际执行的范围，不能延伸成当前数据库或生产环境的成功结论。

## 来源、文档与变更边界

`SOURCE_MANIFEST.json`、各来源 `manifest.json`、`PROVENANCE.md`、`inventory/`、`source/`、`upstream/`、`*-evidence/` 和独立 oracle 固定了来源、许可或历史结果。先读 `scripts/check-source-evidence.py` 识别受保护内容；不要对这些目录批量改名、格式化、翻译或更新哈希来掩盖差异。普通可编辑 Go 实现与来源快照必须区分。

当前范围和来源闭环分别由 [all-drafts 范围索引](all-drafts-completion-scope.json) 与 [来源闭环](all-drafts-source-closure.json) 记录。`architecture.md`、`authenticated-backend-host.md`、`owned-backend-checkpoint.json` 等保留较早快照；它们的旧能力清单、测试数量和限制仅对应记录时的版本。当前架构入口以本页、README 和运行源码为准。旧 README 的完整内容见 [归档](history/README-before-hop-20261018.md)。

本轮 HOP 重构保持已有 HTTP 路径/字段、错误序列化、路由优先级、身份验证、数据库协议和默认启用策略。后续整理优先消除重复与含糊命名，围绕实际业务动作拆分；授权新结构不等于授权更改已有账户数据、接口契约或外部服务。
