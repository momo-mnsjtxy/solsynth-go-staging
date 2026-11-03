# 同步 Solian 上游（2026-11-02）

这次从 [SoSYS/DysonNetwork](https://src.solsynth.dev/SoSYS/DysonNetwork) 的 `4ee2d0ce` 对照到 `74aeaa76`，共检查 11 个新增提交。客户端路径以 [SoSYS/Solian](https://src.solsynth.dev/SoSYS/Solian) `c430ad0e` 为准。GitHub 的 DysonNetwork 镜像比原基线更旧，未用它判断最新状态。

本次改动：

- 新增 `/stargate` 下的账户看板、成长、状态、活动、空间、纪念日、NFC 和钱包路径。它们使用同一份业务与数据，继续执行原有权限检查；登录、OAuth 和签名协议保留自己的入口。
- 开发者应用与机器人凭据的敏感操作要求最近的密码及已启用的第二因子验证。钱包管理读取和订单应用分别使用 `admin.wallets.orders.view`、`admin.wallets.products.view`、`admin.wallets.orders.manage`，普通订单权限不能管理其他用户的账单。
- OAuth 注册和授权支持 `*`、`posts.*`、`chat.*` 等资源范围。授权前展开为现有可委派的具体权限，授权确认页、令牌和持久授权记录使用同一份结果；不会授予管理员权限，也不能越过账户的拒绝规则。`email` 和 `offline_access` 仍需单独申请。
- 读取网页预览需要登录，包括已缓存的结果；现有 HTTPS、DNS 和逐次跳转检查保留。Solian 自链接不再重复抓取或生成博客预览卡片。
- 草稿列表默认排除待发布的定时帖，`include_scheduled=true` 合并显示；按最后修改时间排列合并结果。定时发布继续使用持久任务和订阅通知。
- 普通发帖和定时发布都会为回复、链帖记录持久的帖子关注通知，接入收件箱、实时重放和现有外部推送队列。生成、读取、重放和推送前重新检查当前可见性、屏蔽关系、关注来源和通知偏好。取消收藏、撤销反应或关闭对应偏好后，旧通知会按当前状态过滤；长帖子链按批次处理。
- 连续签到每 7 天奖励 1 gold、每 30 天奖励 5 gold，210 天奖励 6 gold。按连续 UTC 日期计数，补签自身不发金点。奖励写入账本，由后台从显式国库转账；余额不足时保留待支付记录，签到照常成功。

签到金点需要配置已有的国库账户及其钱包：

```bash
--enable-check-in-streak-gold \
--check-in-gold-treasury-account <account-uuid> \
--check-in-gold-treasury-wallet <wallet-uuid> \
--check-in-gold-max-per-day 10000
```

币种固定为 `golds`。不开启时不会划转资金；原有 SourcePoints 配置独立保留。不会自动创建国库或充值。

二次验证沿用客户端 SDK 的 `POST /stargate/auth/sudo` → 获取因子 → 验证密码及第二因子的流程，完成后原会话可重试敏感操作，不需要更换令牌。没有启用 OTP 或恢复码时，可用已验证邮箱接收验证码，需要配置邮件提供方。提升有效期为 5 分钟；刷新会话、修改凭据或因子策略后要重新验证。普通密码登录、OAuth 和 API key 不会自动获得提升。

本次新增的表由 `internal/authoritypg/sudo_schema.sql`、`internal/signinpg/sudo_schema.sql` 以及更新后的 `internal/publishernotificationpg/schema.sql` 定义，已接入临时数据库安装脚本。实际数据库仍需按原有显式安装流程准备；服务启动不会自动建表。

上游把社交关系、状态和活动从 Passport 搬到 Stargate；Go 后端原本就在同一进程维护它们，因此保留现有数据与事务，补客户端新路径。上游跨服务故障时的宽松回退没有搬入，本地数据库错误仍明确失败。支付提供方、生产迁移和旧版协议兼容继续保持原范围。

Stargate 和 Blade 的当前仓库不在公开列表中，匿名读取要求登录。本次未声称同步这两个仓库不可读的内部实现；客户端迁移路径有可读的授权来源。旧来源、许可证和测试记录原样保留，新来源在 `inventory/upstream-sync-20261102/`。

本次实际测试结果将在本页补入，不沿用之前版本的通过记录。
