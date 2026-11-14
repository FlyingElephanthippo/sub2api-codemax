# IP自助放行生产发布记录

2026-11-13 23:51（北京时间）完成上线。用户已审阅本地界面并明确授权发布。

- 生产源码：`67b31fc06ff38392b9ab9a645fdc7ea258133546`
- 生产 tag：`prod-2026-11-13-ip-self-service`
- 应用版本：`0.2.18-ip-self-service`
- 镜像：`local/sub2api:ip-self-service-67b31fc-20261113`
- 镜像 ID：`sha256:414052e22b089a8bea189cf57c6499bfaee7cf85374f216771b2fe1f4b8737a6`
- 实际运行程序 SHA-256：`3d890247afeccd1be9f91aec74de7905efabc332a142a84d6d9011084f4eb13b`
- 双栈策略 SHA-256：`0fcfe35ed1fbd5bcfdb5f79aab1041409ab58225b8b5c184efd105b2f4ddb381`
- 生效 Caddyfile SHA-256：`3da1108f66121b0a96ebbf6983200577a1e814f3d0d53fed34b1cbc797a82758`
- 持久 nft 规则 SHA-256：`84bb42c2d82b0ee50f3ef4ed5db418e404606deffb5dc9871bc447732a6a5256`

以上程序 hash 在运行容器内核对，程序内的 Commit 与生产 tag 指向的源码一致；镜像继承已运行的基础镜像，只替换应用程序。嵌入前端来自最终“IP自助放行”构建。公开发布记录不包含实际地域集合、人工 IP、凭据、数据库备份或生产配置正文。

## 实际变更

新增 migration `251_user_ip_grants.sql` 正常由应用启动执行，迁移记录由 309 增至 310。启用本功能和固定 hash 的策略文件；应用只监听 `127.0.0.1:8080`，通用可信代理收紧到精确回环网段，同时将持久化旧转发头兼容开关关闭。

Caddy 覆盖来源头并移除可伪造头，对其他网站维持原地域及人工名单规则；Sub2API 签名图片先检查网络准入，之后仍由存储服务验证签名；公网内部检查端点返回 404。未知 Host 有拒绝兜底。

nft 只迁移共享 443 的地域拒绝，80 的原规则保留，8080 增加非回环拒绝。人工名单和国内双栈集合没有改变。持久规则已同步，启动加载脚本改成一个 nft 事务替换整表，避免原先先删后载的空窗；未发现现有自动地域更新任务。

## 发布过程与验证

先备份旧程序、容器信息、Compose、环境、应用配置、数据库结构/迁移账本、相关设置、Caddy 和 nft 规则。备份位于主机受限发布目录；旧镜像保留。没有停止或重建 PostgreSQL、Redis，两者启动时间和镜像均保持不变。

原 nft 地域保护仍生效时先加载 Caddy 过渡配置，短暂拒绝新生成请求和新 WebSocket，保留查询。确认普通 API 并发、等待计数、Redis 异步图片、批量任务及队列归零后切换应用。应用切换开始于 15:51:26 UTC，健康及初步验证完成于 15:51:35 UTC，约 9 秒；最终入口与 nft 迁移于 15:51:46 UTC 完成并恢复新请求。

执行的主要检查与操作：

```text
go test -p=2 ./internal/server/middleware ./internal/server/routes -count=1
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags embed -p=2 -trimpath -buildvcs=false -ldflags '<版本、Commit、日期>' ./cmd/server
caddy validate --config <完整过渡/最终候选> --adapter caddyfile
caddy adapt --config <完整候选> --adapter caddyfile
python3 verify_candidate_isolated.py
nft -c -f <迁移事务>
nft -c -f <持久规则原子替换事务>
docker build --pull=false --network=none ...
docker compose up -d --no-deps --pull never sub2api
systemctl reload caddy
nft -f <迁移事务>
```

完整候选 Caddy 在不连接宿主网络的独立 namespace 内通过 85 项检查：IPv4/IPv6、人工及自助准入、伪造头、其他站点隔离、签名图片原 URI 保留、内部请求清除凭据、未知 Host 拒绝。使用合成上游，未调用生产生成接口。

生产应用内部准入及 `/v1/sub2api/billing` 无计费验收：未登记国内 IPv4/IPv6 返回 403，国外和人工名单返回 200；原 Key 鉴权仍执行。没有修改真实用户账目或登记名单来构造测试资格，没有调用付费生成上游。用户登记接口无登录返回 401。

生产实际 TLS 检查：页面和公开设置 200、功能开关为 true、内部检查公网路径 404、未知 Host 404。本机也通过正常域名直接连接页面，返回 200；公网 8080 不可达。Caddy 磁盘适配结果与运行配置完全一致。应用健康、重启计数为 0，启动日志未见迁移/策略加载失败或 panic。

最后文案改名后补齐网关测试的旧提示断言，完整 middleware/routes 回归已重新通过。此前前端 18 项、152 项 Go 定向及 122 项 embed 检查结果见 CHANGELOG；未运行整个项目所有测试，GitHub 未返回 CI 检查结果，不声称远端 CI 已通过。

## 后续限制

独立支付站点维持原地域限制，支付跳转可能仍需原有可访问网络。此次没有实际验证通知邮件投递、生产用户新增/替换/删除完整流程，亦没有进行多地区运营商或 HTTP/3 实际客户端测试；HTTP/3 共用已核对的 Caddy 路由。美国服务器与当前本机直连成功不代表所有国内网络长期可达。

## 回滚

旧应用镜像：`local/sub2api:encrypted-context-28ed28c93914-20261112`。旧程序 SHA-256：`4d9d22c50b758b6cc09a176a5962168204e0d7ec359210bc85d18fa0f92c122e`。

发布目录保存可执行回滚脚本和备份。必须先用单事务恢复 nft 80/443/8080 地域拒绝结构，保留回滚时最新人工集合，并同步持久规则，再恢复原 Caddy、相关设置和应用 Compose。只重建应用，保留新增表与审计，不恢复旧数据覆盖并发业务。不要先关闭应用准入，也不要先回退旧应用。

生产 tag 指向实际部署代码；本发布说明作为之后的文档提交保存，不伪称文档提交已重新部署。
