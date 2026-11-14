# 管理员动态 IP 放行与地域拒绝行为

状态：2026-11-14 18:07（北京时间）已完成用户授权的生产发布。首次应用切换与配套 Caddy 重载已完成，后续增删动态记录无需重启。实际验证和维护窗口见 [生产发布记录](PRODUCTION-2026-11-14.md)。

## 行为

- 管理员通过 `/admin/ip-grants` 添加或撤销单个公网 IPv4/IPv6，可填写备注；不绑定被放行用户、不检查其充值资格。
- 动态授权与既有固定名单、有效自助授权并存。每次国内准入实时查询，新增或撤销提交后立即可见；移除一条动态记录不会删除其他来源的有效授权。
- migration `252_admin_domestic_ip_grants.sql` 新增独立名单与审计表。实际增删与审计同一事务，管理员身份在事务中重新检查，并发和重复操作幂等。
- 大陆来源默认拒绝，国外来源继续正常准入；API Key、余额、用户状态和存储签名等检查继续执行。
- 地域限制从网络层迁到应用层后，客户端会看到明确的 403。配套 Caddy 仅对可信应用的地域拒绝执行连接中断，使客户端可沿原有网络重试流程处理。普通鉴权或业务错误保留，内部标记不能由客户端或外部上游伪造。

此功能覆盖 Sub2API 业务及已接入准入检查的签名图片，不修改其他独立网站的名单。客户端重试仍有次数限制，合成测试不代表真实 Codex 桌面任务一定恢复；服务端没有盲目重放已提交的模型请求。

## 验证

- 三轮定向 Go 测试去重后 148 项测试及子测试通过，0 失败、0 跳过；包含真实本地 PostgreSQL 的即时增删、并发、事务审计和失败回滚。
- 管理页面 9 项组件测试、3 项国际化检查、ESLint、类型检查与生产构建通过。浏览器验证新增 IPv4/IPv6、搜索、撤销、私网拒绝和桌面/手机布局。
- Python 导出器 5 项测试通过。真实 Caddy 2.6.2 验证 HTTP/1.1 与 HTTP/2、签名图片、WebSocket 升级前拒绝、普通错误保留、HTTP/2 并发流隔离，以及模拟恢复后仅派发一次生成。
- 完整候选 Caddy 配置通过 `validate`、`adapt`；路由树比较仅主应用和资产准入增加精确拒绝处理。Linux amd64 嵌入前端构建、`git diff --check` 通过。

主要命令（Go 命令在 `backend`、前端命令在 `frontend` 执行）：

```text
go test -tags=domestic_ip_integration -p=2 ./internal/pkg/domesticip ./internal/service ./internal/repository ./internal/handler ./internal/handler/admin ./internal/server/routes ./internal/util/responseheaders -run 'Test(AdminDomesticIP|DomesticIP|NetworkAdmission)' -count=1 -json
go test -tags=domestic_ip_integration -p=2 ./internal/handler ./internal/server/routes ./internal/util/responseheaders -run 'Test(DomesticIP|NetworkAdmission)' -count=1 -json
go test -p=2 ./internal/service -run '^TestAdminDomesticIP' -count=1 -json
pnpm exec vitest run src/views/admin/__tests__/DomesticIPGrantsView.spec.ts src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm exec eslint src/api/admin/domesticIp.ts src/views/admin/DomesticIPGrantsView.vue src/views/admin/__tests__/DomesticIPGrantsView.spec.ts src/i18n/locales/en/adminDomesticIp.ts src/i18n/locales/zh/adminDomesticIp.ts
pnpm build
python -m unittest discover -s deploy/domestic-ip -p test_export_policy.py
python deploy/domestic-ip/verify_caddy_local.py --caddy <Caddy 2.6.2>
python deploy/domestic-ip/verify_transport_retry.py --caddy <Caddy 2.6.2>
```

集成测试需要独立的 `DOMESTIC_IP_TEST_DATABASE_URL`；协议测试使用 `httpx[http2]` 和 `cryptography`。未运行整个项目全部测试，未做客户桌面版恢复或国内多运营商实测。上线后已核对动态记录、审计、无计费准入及真实应用与隔离本机 Caddy 的 HTTP/1.1 连接中断；HTTP/2 证据仍为上述本地合成验证。

## 增量发布

保持原地域快照不变，先暂停新生成请求并等待在途任务与异步队列排空，仅替换应用容器，再启用配套 Caddy 片段；数据库和 Redis 无需重启。回退到此次发布前的应用/Caddy，保留新增表与审计、现行防火墙和准入开关。旧版本不识别新动态记录，回退后仅依靠该记录的来源会再次受限。

具体配置及适用范围见 [README](README.md)。不要把首次自助准入架构迁移的防火墙回退步骤用于本次增量更新。
