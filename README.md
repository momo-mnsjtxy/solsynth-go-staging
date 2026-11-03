# Solian Go 后端

Solian / Solsynth 的 Go 后端，在一个进程中提供账户、内容、聊天和钱包等 HTTP API，使用 PostgreSQL 持久化数据。主程序位于 `cmd/backend`，客户端单独维护。

当前业务包括：

- 账户与身份：登录、会话、OAuth、个人资料、好友、空间和成长。
- 内容与聊天：帖子、时间线、互动、房间、消息同步和实时通知。
- 钱包与商业：账户余额、转账、订单、订阅、商品和商家管理。
- 扩展能力：媒体、开发者应用、机器人、通话、直播和联邦；需要提供方的功能按配置启用。

## 安装

准备 Go **1.26.4**。在仓库根目录执行；如果 Go 不在 `PATH` 中，将 `GO_BIN` 设为编译器的绝对路径。

```bash
export GO_BIN="${GO_BIN:-go}"
"$GO_BIN" version
"$GO_BIN" mod download
"$GO_BIN" mod verify
mkdir -p .cache/bin
"$GO_BIN" build -buildvcs=false -o .cache/bin/backend ./cmd/backend
```

运行服务还需要已准备好表结构的 PostgreSQL 数据库，以及与令牌签发方匹配的 RSA 公钥。程序不会自动建表、创建账户或生成密钥；数据库准备方式见 [配置说明](docs/configuration.md#数据库)。

## 配置与启动

以下是 Bash 示例。先替换数据库 URL、公钥路径和身份配置；聊天枚举必须与目标数据库一致，示例使用未加密模式 `0`，通知模式 `0`、`1`、`2`。分类公开值为 `0`。

```bash
export SOLSYNTH_BACKEND_DSN='postgresql://solian_dev:replace-me@127.0.0.1:5432/solian_dev?sslmode=disable'

backend_args=(
  --issuer 'https://issuer.example.test'
  --audience 'solian-development'
  --key-id 'development-key'
  --trusted-public-key-file '/absolute/path/to/trusted-public.pem'
  --taxonomy-public-visibility 0
  --messager-none-encryption 0
  --messager-notify-all 0
  --messager-notify-mentions 1
  --messager-notify-none 2
)

# 先校验配置，不连接数据库。
.cache/bin/backend "${backend_args[@]}"

# 校验成功且数据库已准备好后，在同一终端启动服务。
.cache/bin/backend "${backend_args[@]}" --serve --listen 127.0.0.1:8090
```

校验成功会输出 `Backend configuration validated.` 开头的消息。示例中的关闭 TLS 只适用于本地开发；远程数据库要求 `sslmode=verify-full`。默认配置只验证已有令牌，启用登录、OAuth 或外部服务时，按 [可选功能](docs/configuration.md#可选功能) 补充配置。

## 使用

服务启动后，在另一个终端检查：

```bash
curl --fail http://127.0.0.1:8090/healthz
curl --fail http://127.0.0.1:8090/readyz
```

`/healthz` 检查 HTTP 服务是否响应，`/readyz` 检查数据库及已启用模块需要的表和部分约束。取得当前后端的访问令牌后，可读取自己的账户：

```bash
curl --fail --header 'Authorization: Bearer <access-token>' \
  http://127.0.0.1:8090/api/accounts/me
```

对外访问通过可信的 TLS 反向代理转发到本机监听地址。按 `Ctrl+C` 或发送 `SIGTERM` 可停止服务。

更多参数用 `.cache/bin/backend --help` 查看。启动报错时查 [常见问题](docs/configuration.md#常见问题)；修改功能、运行测试时读 [开发指南](docs/development.md)。其他文档从 [文档目录](docs/README.md) 查找。

许可证：[AGPL-3.0-only](LICENSE.txt)，第三方声明见 [NOTICE.md](NOTICE.md)。
