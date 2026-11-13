# 国内 IP 自助放行：方案 A 的发布准备

这份工具只生成本地候选文件，不连接服务器、不写 nftables、不重载服务。它不是已执行的生产部署，也不能证明国内各运营商线路可达。

## 同一份地域数据

由经过授权的只读采集获得当前 `nft -j list table inet sub2api_cn_block` JSON，保存到仓库之外的私有文件。输出包含人工名单，不能上传 GitHub。

```powershell
python deploy/domestic-ip/export_policy.py --input D:/private/nft.private.json --output-dir D:/private/policy-REVIEWED_VERSION
python -m unittest discover -s deploy/domestic-ip -p test_export_policy.py
python deploy/domestic-ip/verify_caddy_local.py --caddy /path/to/caddy
```

输出目录必须事先不存在。工具接受 `cn_ipv4`、`cn_ipv6`、`allow_ipv4` 和可选的 `allow_ipv6`，支持 nft JSON 单地址、prefix、range 和 elem 包装，拒绝临时授权、私网、保留网段、缺少地址族和人工名单里的 CIDR。输出为：

- `snapshot.json`：应用使用的地域 CIDR 与人工单 IP 名单；不含用户身份或密钥，但仍是不能公开的运营数据。
- `other-sites.caddy`：标准 Caddy `remote_ip` / `not remote_ip` 匹配器，不依赖插件，用于保留其他网站的国内拦截；同时包含专供 Sub2API 签名图片路径使用的 `forward_auth` 片段。
- `manifest.json`：上述文件 hash、地址族数量和人工名单数量；可以核对，不列出具体地址。

应用开启时必须提供 `DOMESTIC_IP_SNAPSHOT_FILE` 与准确的 `DOMESTIC_IP_SNAPSHOT_SHA256`。错误 hash、不完整或非法数据会使构造策略失败，调用方必须将这个错误作为启动失败，不能退回“全部放行”。默认 `DOMESTIC_IP_ENABLED=false` 不加载文件，适合上线前预览与现有部署兼容，**不是已迁移入口后的安全关停开关**。

快照不在后台自动拉取或自动过期。后续地域更新、人工名单增加和服务重启，都须通过同一版本工作流同时更新应用、Caddy 和持久化 nft 生成入口。更新失败保留上一个经过验证的完整版本。维护方须检查已有 systemd / cron / timer，防止旧更新任务重新把共享 443 拦死。

## Caddy 路由保护

全局导入生成文件后，在所有非 Sub2API 网站或同域名的非 Sub2API 分支中导入 `domestic_ip_other_sites`，并置于实际反代之前。不能只按域名猜测归属：图片存储、文档及其他反代可能位于同一站点下不同路径。

示意（域名和端口仅为占位，不可直接覆盖现有配置）：

```caddyfile
import /etc/caddy/domestic-ip/REVIEWED_VERSION/other-sites.caddy

other-service.example {
    route {
        import domestic_ip_other_sites
        reverse_proxy 127.0.0.1:9000
    }
}

api.example {
  route {
    import domestic_ip_block_public_internal
    # /assets/* 和 9001 仅是示意，发布时必须核对实际桶路径与上游。
    handle /assets/* {
        route {
            import domestic_ip_sub2api_assets
            reverse_proxy 127.0.0.1:9001
        }
    }
    # 所有既有非 Sub2API 分支分别保留原保护；本示意省略其他实际分支。
    handle {
        reverse_proxy 127.0.0.1:8080 {
            header_up X-Forwarded-For {http.request.remote.host}
            header_up X-Real-IP {http.request.remote.host}
            header_up -CF-Connecting-IP
        }
    }
  }
}
```

对真实完整候选 Caddyfile 执行 `caddy validate` 和 `caddy adapt`，用适配后的配置逐分支检查，而非只验证上面的片段语法。特别检查：

- 每个已有 host、path、反代目的地、静态资源、WebSocket、图片存储以及 catch-all 是否有正确策略。
- 未知 Host / SNI 和直接 IP 访问不能落入未保护的其他服务；显式拒绝所有非预期兜底路由。
- 依赖拒绝顺序时用显式 `route` 包住拒绝规则与后续 `handle` 分支。Caddy 默认会按指令类别重新排序；把 `respond` 写在 `handle` 前面并不能证明它先执行，必须检查 adapt 后顺序并做实际请求测试。
- HTTP/3 使用 UDP 443，旧的 TCP 规则不能证明 UDP 同样受控。新 Caddy 路由门禁应对所有启用的 HTTP 协议生效；同时检查现有 UDP 入站和监听，不能只测 TCP。
- `remote_ip` 读取真实 TCP/QUIC peer，不受伪造 XFF / CF 头影响。此方案只适用于用户直达同机 Caddy；未来增加 CDN/代理必须重新设计信任链，不能直接相信客户端头。
- Sub2API 只监听回环，可信代理限定准确回环地址；环境变量和后台持久化开关也要同步核对，不能仅修改 YAML 就声称已关闭旧兼容模式。

Sub2API 所属的签名图片不能当作普通“其他服务”一直只允许人工名单，否则自助授权用户虽能生成图片，却取不到图片。生成的 `domestic_ip_sub2api_assets` 通过固定回环地址调用 `/api/v1/internal/domestic-ip/access` 验证当前来源，之后才把原始请求转给对象存储。该检查仅验证地域准入，不能替代存储服务本身的签名授权；保持原 Host、桶路径、查询串和签名验证，禁止变成匿名开放桶。每条真实业务资产路径都需明确归属，不能将这个片段应用到与 Sub2API 无关的存储或网站。

内部准入端点应在禁用、未接线或数据不可用时返回失败，禁止暴露成一个由客户端提供 IP 的任意查询接口。`forward_auth` 使用末尾带 `?` 的 URI 清空原签名查询串，并删除 Authorization、Cookie、API Key 和 CF 头，只转发 Caddy 覆盖的真实来源；对象存储仍收到完整原请求，签名数据不会复制进准入检查请求。

准入 URI 必须保留 `/api/v1/` 前缀，不能缩写成 `/internal/...`：应用内嵌前端的 SPA 回退可能把未知页面返回 HTML 200，Caddy 会把这个 200 当作已获准访问。

公网 Sub2API 路由的显式 `route` 内首先导入 `domestic_ip_block_public_internal`，拒绝客户端直接访问内部准入 URI。内部 `forward_auth` 直连应用回环端口，不经过该公网拒绝规则。这可避免匿名客户端直接制造无 API Key 的数据库资格查询；仅依靠应用看到回环 peer 无法区分合法 Caddy 子请求与被 Caddy 转发的公网直请求。

当前发布盘点的分类应为：Sub2API 主应用、其 `/codemax-async-images` 桶路径属于业务准入；muse、cpa 与支付独立站点仍维持原人工名单/国外来源策略。这里仅记录路由职责，不携带真实人工 IP。独立支付站点暂不增加国内公开权限，因此不能宣称“未放行国内用户的完整充值跳转链路已经可达”；发布说明需明确支付跳转可能仍需原有可访问网络。

## 分阶段发布与回滚

生产每一步均须用户明确授权并在约定窗口执行，当前文件不授予授权。

1. 先记录当前运行镜像、程序/资源 hash、数据库迁移、Caddy 配置、nft 实时和持久规则、人工集合及地域更新机制，备份并校验。
2. 在隔离环境使用同版本 Caddy 验证候选配置、两种地址族、伪造转发头、所有路由和失败模式；真实国内出口另做端到端验收。
3. 保持原 nft 国内拦截仍生效，先发布带业务准入的新应用并装入已校验的 snapshot。应用启用后，即使尚未迁移防火墙，也不能让原人工名单和国外来源被误拦。
4. 先加载其他网站的等效 Caddy 门禁，确认不存在开放路由；再在一个受审查的 nft 事务内迁移共享 443 国内拒绝规则，并同步持久化规则/生成器。80 的原策略保持，8080 的公网入口必须同时收紧。
5. 迁移完成后再验证国内控制台可达、未授权国内业务拒绝、自助授权后业务可用，其他服务仍保持原国内限制；核对原 443 登录、会话绑定与国外 API 行为。
6. **回滚先恢复原 nft 国内拒绝结构，再回退 Caddy 和应用**。恢复时保留当前最新人工集合，不能整份覆盖旧备份而删除维护期间新增名单。同步恢复持久化/启动/自动更新机制；保留新增业务表和审计。

不得先将 `DOMESTIC_IP_ENABLED=false` 再恢复网络拦截；关闭应用准入而让共享 443 保持开放会形成保护空窗。

## 已覆盖的本地校验

Go 工具包测试覆盖公网规范化、保留地址拒绝、双栈/IPv4-mapped、严格回环代理、单值 XFF、伪造其他头、重复头、数据缺失、hash 不匹配、禁用兼容与人工名单优先。

Python 测试覆盖 nft JSON 常见元素格式、双栈完整性、单 IP 人工名单、临时集合拒绝与应用/Caddy hash 一致性。真实完整 Caddy 配置验证、生产持久化迁移和真实国内网络测试属于发布前必做项目，工具单测不能替代。

`verify_caddy_local.py` 只绑定本机回环，启动临时 Caddy 和无认证的合成测试后端，结束时停止这两个进程。已用官方 Caddy 2.6.2 实际验证：同域非 Sub2API 分支拒绝、登录分支仍可达、重复/伪造转发头被覆盖、CF 头移除、未知 Host 被拒绝；签名资产无授权时拒绝、有授权时保持原签名 URI 转发，内部检查请求无原签名查询或认证凭证。测试中的 127/8 地域规则仅用于模拟国内来源，真实导出器不会允许这种数据。两个生成样式（整站门禁、同域路径门禁）也已通过该版本的 `caddy validate`；这些结果不代表生产完整配置已经验证。
