# IP自助放行：开发交付记录

状态：2026-11-13 已经用户授权上线，生产 tag 为 `prod-2026-11-13-ip-self-service`。实际部署与核验见 [生产发布记录](PRODUCTION-2026-11-13.md)。以下保留开发阶段的范围和验证记录。

按用户最后确认，页面、入口和用户提示统一命名为“IP自助放行”。准入规则仍按所选路线 A 实施。

## 功能与范围

新增 `/ip-grants` 用户页，累计净实付人民币余额充值满 200 元可登记当前公网 IP。每用户最多 3 个，按北京时间自然日最多 5 次成功新增、删除或替换；每次操作均有二次确认与事务内审计。页面不接受手填地址，提交的 `expected_ip` 只用于检测确认期间的网络变化。

名单解除国内来源的业务访问限制，不限制该用户在其他原本可用网络中使用 Key。国外来源与既有人工名单沿用原准入规则；共享 IP 由仍有资格的登记取并集。退款后不满足资格的登记保留但暂停放行，重新达标后自动恢复。状态由支付记录实时计算，不额外产生“系统暂停”事件；用户操作审计完整保留。

按 `payment_orders.pay_amount` 计算实收，成功退款按原退款流程的比例和人民币舍入扣除。`amount/refund_amount` 是到账额度单位，不能直接与实收相减；不计赠送、返利、订阅购买或缺少付款证据的管理员加款。历史币种缺失沿用现有代码的 CNY 缺省值。

两条 API 鉴权链统一执行地域准入，覆盖 OpenAI / Gemini / Codex / Antigravity、根路径别名、任务查询和 WebSocket。面板的图片内容读取同样检查；独立签名图片路径使用 Caddy `forward_auth` 检查来源后，仍保留存储签名授权。该内部接口必须在公网 Caddy 阻断。

## 文件与数据

- `backend/internal/pkg/domesticip/`：公网地址校验、可信回环代理、地域和人工名单策略。
- `backend/internal/service/domestic_ip_*.go`、`repository/domestic_ip_*.go`：净实付资格、事务、限额、网络准入和通知。
- `backend/internal/handler/domestic_ip_handler.go`：仅操作当前登录用户的记录，比较确认 IP，返回最新状态。
- 两条 API Key 鉴权、用户路由、图片内容路由、依赖注入及公开设置增加最小挂接。
- `frontend/src/views/user/DomesticIPView.vue`、API / 路由 / 侧栏 / 中英文文案：完整控制台页面。
- `backend/migrations/251_user_ip_grants.sql`：`user_ip_grants` 与 `user_ip_grant_events`；用户+IP唯一、单地址检查、审计及查找索引。
- 本目录的导出器、样例、Caddy 验证器和发布说明：准备可审查的入口迁移，不自动修改生产。

默认 `DOMESTIC_IP_ENABLED=false`，不改变旧部署的业务准入。开启时必须提供双栈策略文件及固定 SHA-256，缺失或损坏即启动失败。该默认关闭模式不能用于已经迁移完防火墙后的直接回滚；正确顺序见 README。

邮件复用既有服务，最多 4 个并发，不阻塞修改响应。审计保存 pending / sent / failed / skipped；若进程退出时邮件未完成，可能保留 pending，本功能没有新建持久化邮件队列。

## 生产源码基线

开发来源：`709e4c824143f53347d2a7288904d3b426cb6824` 加已上线补丁，补丁 SHA-256：

`28ed28c93914a8c006f1e68c563d674bd49139ce7c88fb7d166adce25b101820`

本地合并归档引用：`9b9c607ddb267728d706b31eb9fedc4cf55bb297`。公开归档仓库以该引用建立独立基线提交 `eeb0f3477797e06938841efc320fe2829d7e1db0`，不带入其他本地开发历史。公开基线排除两份本站运维记录 `NEXT.md`、`docs/muse-integration.md`；其余 4422 个文件的 Git blob 与文件模式均逐一核对，和原始引用完全一致。完整原始快照只保存在本地，应用源码与许可证未删改。

核对中的生产程序 SHA-256：

`4d9d22c50b758b6cc09a176a5962168204e0d7ec359210bc85d18fa0f92c122e`

历史构建复用了嵌入前端资源，源码树与程序 hash 是独立证据。基线归档不声称已重新构建出逐字节相同的历史二进制。开发阶段的 Linux 构建用于编译检查；最终生产构建及其 Commit/hash 另见生产发布记录。

## 实际本地验证

```text
pnpm install --frozen-lockfile
pnpm typecheck
pnpm exec vitest run src/views/user/__tests__/DomesticIPView.spec.ts src/utils/__tests__/featureFlags.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm build
go run github.com/google/wire/cmd/wire ./cmd/server
go test -tags=domestic_ip_integration -p=2 ./internal/pkg/domesticip ./internal/config ./internal/service ./internal/repository ./internal/handler ./internal/handler/dto ./internal/server/middleware ./internal/server/routes ./cmd/server -run 'TestDomesticIP|TestNormalizePublicIP|TestPolicy|TestValidateSnapshot|Test.*PublicSettingsInjection|Test.*TrustedProx|Test.*Forwarded' -count=1
go test -tags=embed -p=2 ./internal/service ./internal/handler ./internal/handler/dto ./internal/server/routes ./internal/web -run 'TestDomesticIP|TestPublicSettingsInjection|Test.*Bypass|TestFrontendServer_Middleware' -count=1
python -m unittest discover -s deploy/domestic-ip -p test_export_policy.py
python deploy/domestic-ip/verify_caddy_local.py --caddy <本地官方 Caddy 2.6.2>
```

第一组 Go 定向验证通过 152 个测试及子测试，无跳过；PostgreSQL 测试实际运行于本地独立数据库，覆盖实付边界、退款、并发 3 IP / 5 次限制、共享授权、日界与回滚。前端 18 项检查通过。浏览器验证了添加、替换、删除、199 元差额、次数用尽、非法 IP、桌面 / 手机和深浅主题；预览全部使用合成数据，不调用生产业务。

完整 `go test -p=2 ./internal/server/middleware ./internal/server/routes -count=1` 回归通过；`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embed -p=2 -trimpath ./cmd/server` 编译通过。未运行整个项目全部测试，不将定向验证冒充全量验证。

## 尚需生产验证与回滚

已完成完整生产 Caddy 候选检查、实时及持久 nft 迁移、无计费准入验收和本机域名 HTTPS 直连。尚未执行多地区国内线路、生产用户完整变更流程和邮件实际投递验收。国外机房可能受不同运营商线路或网络限制影响；应用功能不承诺所有国内网络可达。

独立支付站点仍按原国内规则保护，不能据“充值页面可打开”宣称整个支付跳转链路国内直连可用。其暴露范围需要单独确认。

回滚顺序：先恢复国内网络拒绝结构，并保留回滚时最新人工名单，再恢复 Caddy / 应用版本及对应持久化规则生成机制。新增表和审计保留，不通过删数据回滚。不得先关应用准入、后恢复防火墙。

## 升级后重放补丁

本功能集中于独立文件，现有文件主要增加依赖、路由与鉴权挂接。发布分支相对生产基线的 `git diff --binary` 可以单独保存为补丁。升级时先在新上游基线运行 `git apply --check`，处理必要冲突，再执行上述验证；不能直接覆盖生产源码或改写已应用迁移。发布记录应另存目标 commit、二进制与资源 hash，实际部署成功后才打生产 tag。
