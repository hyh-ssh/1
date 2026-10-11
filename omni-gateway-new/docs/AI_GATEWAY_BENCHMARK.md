# 与 1Panel AI Gateway 的对标分析：本项目劣势与可借鉴项

对比对象：本项目 `omni-gateway` 与相邻的 `ai-gateway` 参考项目
（对 `1panel/ai-gateway:v1.0.1` 的逆向分析 + 干净室兼容实现）。
对标基线：本项目分支 `feat/oidc-sso-login` 的提交 `5cfb93a`（已包含默认禁用的
`supports_responses` 和管理台开关）；参考项目为 `docs/api-contract.zh-CN.md`、
`docs/reverse-report.zh-CN.md` 与 `reconstruction/` 源码。下文规模数字为调研时快照，不是持续维护的指标。

**证据分级提醒**：参考项目的端点与参数来自二进制逆向与干净室复刻，属「恢复出的契约」，不等于官方文档承诺；
引用其行为时本文以恢复结果为准，不能视作 LiteLLM 或 1Panel 官方保证。本项目的 `file:line` 会随提交漂移，请以符号名检索。

对标调研时实跑 `cd backend && go build ./... && go test ./...`（未设 `TEST_DATABASE_URL` 时 PG 集成测试
自跳过）；`5cfb93a` 的 Go、真实 PG、前端及 HTTP E2E 验证结果见提交历史与测试记录。

---

## 0. 结论摘要

1. **保留本项目已有正确性约束**：两本账（上游成本 CNY 与用户积分）、不可变价格版本、幂等结算 ID、
   `unpriced` / `zero-credit` 可见化、SSRF 感知传输层、OIDC(PKCE)、密钥轮换重加密、CI 双 PG 矩阵。
   参考实现对这些约束的覆盖不同；不能为对齐功能而削弱它们。
2. **参考项目展现了可借鉴的产品化方向**：调度、审计、运行期配置、统计和运维工具；以下能力来自恢复出的契约，
   并非上游官方保证，不应为了端点数量照搬复杂度。
3. **当前优先项改为 OpenAI-like 协议正确性**：确认 Chat SSE 的 `reasoning_content`、工具增量和 usage 是否保留，
   再处理日志保留、并发等待、调度与配置等工程缺口；流式 Chat 400 的历史原始校验原因未获证实，不能归因于任一缺口。
4. **前端与文档仍有欠账**：架构/门禁/接口契约需按真实用例演进；`Task/TaskWait` 的运行进度展示不属于已确认的
   网关能力，不应把其缺少中途反馈直接归咎于网关没有转发模型推理。

---

## 1. 规模与形态对照

| 维度 | 本项目 omni-gateway | 参考项目 ai-gateway（重建/契约） |
|---|---|---|
| 后端非测试代码 | 13,696 行 / 13 包 | 23,491 行 / 22 包 |
| 后端测试 | 10,592 行、51 文件、256 个 `Test` | 44 个 `_test.go`（共 143 个 `.go` 文件） |
| 数据库 | PostgreSQL（`lib/pq` + XORM，7 个内嵌迁移，22 张表） | SQLite（`mattn/go-sqlite3`，`migrations.go` 内 8 个命名迁移） |
| 部署形态 | 单二进制，前端 `//go:embed all:static`（未构建时占位页，`httpapi/ui.go`） | 单二进制，前端构建标签 `embed_webui`（无标签时 503 `webui_unavailable`） |
| 控制台 | Vue 3 源码在仓库内，1,425 生产行 + 1,637 测试行 | 仅内嵌产物，重建仓库内无前端源码 |
| 端点总数 | 约 60（数据面 6 + 认证/自助 + 管理面 17 组） | 105（+3 别名） |
| 运维入口 | 只有 `cmd/server`（无子命令/参数） | `ai-gateway -reset-admin-password` 等标志 + 3 个根 `scripts/*.sh` |
| 依赖哲学 | go.mod 仅 4 个直接依赖（pq / xorm / decimal / x-crypto） | go.mod 3 个直接依赖（sqlite3 / x-crypto / yaml） |

---

## 2. 能力矩阵

判定口径：**领先** = 本项目更强或参考项目没有；**持平** = 双方等价；**落后** = 参考项目更强。

### 2.1 数据面与协议

| 能力 | 本项目 | 参考项目 | 判定 |
|---|---|---|---|
| 兼容端点 | Chat / Responses（仅显式支持的供应商）/ Messages / Embeddings / Images；不支持 Responses 时提前返回 501 | 恢复契约覆盖同五项 | 部分（需逐上游核验） |
| 上游协议种类 | 3 种（`openai_compatible` / `anthropic` / `gemini`） | 11 种协议枚举（含 dashscope/minimax/openrouter/tokenhub/siliconflow 图像等）+ 4 种认证注入（`none`/`bearer`/`xApiKey`/`xGoogleApiKey`） | 落后 |
| 协议转换 | 仅 Messages↔Chat，且**限纯文本**（tool/多模态/未知字段 → 501） | 6 个方向的矩阵（Chat/Responses/Anthropic/Gemini 互转），开关 `gateway.protocol_conversion_enabled`（默认关），**原生 plan 恒排第一** | 落后 |
| 转发哲学 | Chat SSE 解析为 `protocol.StreamEvent` 并重组；字段完整性待端到端回归 | 重建实现含原生字节透传路径 | 不宜直接判定优劣 |
| 故障转移 | 按 `route_entries.priority` 顺序遍历**全部**启用路由，流式首帧后停止换路 | 最多 2 次上游尝试（流式固定 1 次），每次改选**下一个最优候选** | 持平/落后 |
| 冷却与健康 | 指数退避 5s·2^(n-1) 上限 5min，尊重 `Retry-After`，逐 provider 持久化 | 账号级 30s 冷却窗口，探测锁 + 成功回写 `available` | 领先（本项目退避更细） |
| `Retry-After` 解析 | 秒数 + HTTP-date（`adapter.ParseRetryAfter`，上限 5 分钟） | 正整数秒（`>60` 截 60）+ HTTP-date（未来值向上取整），默认 30s、上限 60s | 持平 |
| 请求头处理 | **不转发任何客户端头**：每个上游请求重建，只带 Content-Type / Accept / 凭据 / `extra_headers`（`adapter` 的 `newRequest`、`prepareNative`） | 剥离 31 个敏感/逐跳头（Cookie、CSRF、真实 IP、Via…）后原样转发 | **领先** |
| 响应体上限 | 原生单帧 32MB | 非流式转换缓冲 32 MiB、发现响应截断 1 MiB | 持平 |
| 模型发现 | `POST /providers/{id}/discover|verify` + 探测原因名 | `discover-models` 双端点，返回 `added`/`unchanged`/`missing` + HMAC `evidence`（5 分钟容差） | 落后（证据化差异） |

### 2.2 准入、调度与授权

| 能力 | 本项目 | 参考项目 | 判定 |
|---|---|---|---|
| 预算 | ¥ 成本上限 + 积分上限，硬拒绝 429 | 无成本/预算模型 | **领先** |
| 限流 | RPM / TPM / 并发，按用户与 API Key 作用域 | 全局 + 按 API Key + 按用户组三层并发 | 领先 |
| 排队 | **无**：超限立即 429/503 | `gateway.queue_size`（默认 10）+ `queue_timeout_seconds`（默认 3s）有界等待；按 Key 满 → 429 `concurrency_limit_exceeded`，全局队列满/超时 → 503 `gateway_overloaded` | **落后** |
| 准入可观测 | `/api/admin/runtime` 内存计数器（队列/结算） | `concurrency{current,limit}`、`waitQueue{current,limit}`、`streamingRequests`、`activeUsers` | 落后（本项目缺排队与在飞视图） |
| 选号 | 静态：priority 升序 + 跳过冷却/满并发 | 调度器：健康可选举 → **最小在飞利用率** → priority → 组内轮转；探测锁（`RequestLease` 持有）；**按 API Key 的账号亲和** | **落后** |
| 授权模型 | 用户组 → 模型组 → 别名（`httpapi/groups.go`） | 用户组 → 模型组 → 请求模型名；含 `options` 辅助端点 | 持平 |
| 多租户/组织 | 单组织（显式设计） | 单组织 | 持平 |

### 2.3 计量、统计与可观测

| 能力 | 本项目 | 参考项目 | 判定 |
|---|---|---|---|
| 账务模型 | 两本账：成本（CNY，逐 attempt 计费）+ 积分（仅成功）；价格版本不可变；无价 `unpriced`、零积分 `zero_credit` 计数器 | 只有 token 统计（无价格/账本/预算） | **领先** |
| 结算可靠性 | 幂等 settlement ID + 有界重试队列 + 优雅关闭排空 | 异步 `usage.Record` 单次写入（无幂等 ID、无重试队列） | **领先** |
| 用量聚合 | 单一 `StatsSummary`（分位、路由统计、过滤维度） | 六维聚合（供应商/模型/模型组/上游账号/用户组/用户）+ token 趋势（`range` + `timezoneOffsetMinutes` 对齐的**稠密**序列） | 落后 |
| 请求日志 | `requests` / `attempts` / `usage_ledger` / `audit_log` 全量落库 | `call_logs` + `call_log_attempts`，逐次尝试有行 | 持平 |
| 日志保留 | **无清理路径**；`GATEWAY_LOG_RETENTION_DAYS` 是死旋钮（`config.go` 自注 informational） | `log.call_retention_days` 驱动周期清理 | **落后** |
| 内容日志/审计 | 加密内容日志（默认关）+ `GET /api/admin/content/{id}` 写 `view` 审计 | 请求体审计：开关 + 1–1024 KiB 上限 + 保留 1–365 天 + ES 连接 + `view`/`export` 访问事件 + SHA-256 摘要 + 导出端点 | 落后（产品化差距） |
| Metrics/tracing | 无（显式非目标） | 无 | 持平 |
| 错误信封 | `{error:{message,type,code,request_id}}`，流内错误同构 | 后台 `{code,message}` + 数据面 OpenAI 信封 | 领先 |

### 2.4 治理、智能与配置

| 能力 | 本项目 | 参考项目 | 判定 |
|---|---|---|---|
| 内容合规 | 无（grep `compliance|moderation|guardrail` 仅命中 gemini 的 `SAFETY` finish reason） | 关键词（Aho–Corasick）+ 语义余弦两级，`allow`/`audit_only`/`block` 三级决策，命中 `block` → 403 `content_policy_violation`，审计异步落库，独立四表 | **落后** |
| 智能路由 | 无（选路只有 priority + 冷却降级） | `auto` 模型名 + 复杂度分类器（特征加权，阈值 0.72，`score×0.24` clamp[0.05,1]）+ 简单/复杂模型组回退 + 决策日志 20 字段 + 日统计 UPSERT + 188 条幂等 seed 样本 + 预览端点 | **落后** |
| 向量服务 | 无（`/v1/embeddings` 仅透传） | 向量服务配置/校验/连通性测试 + 有界并发向量任务管理器（启动/查询/取消/剪枝） | **落后** |
| 运行期配置 | 全 env，`config.Load()` 只在启动调用一次 | `settings(key,value)` + 运行时事务（`BeginTx→change→Commit`），9+ 项**请求级热读**（`accessBaseURL`、`protocolConversionEnabled`、`logRetentionDays`、`maxConcurrency`、`queueSize`、`queueTimeoutSeconds`、`requestTimeoutSeconds`、`streamIdleTimeoutSeconds`、`maxBodyMb`）+ 写后触发智能路由/向量服务重载 | **落后** |
| 软删除 | 无用户 DELETE；无删除标签回填 | `deleted_resource_labels`：删除后列表回填 `deleted:true` 与空名 | 落后 |
| 本地化 | 无（前端 `errors.ts` 中文映射表，API 错误体英文） | `Accept-Language` 协商 + `Content-Language`/`Vary`（错误体仍英文） | 落后（价值有限） |
| 认证面 | 口令 + Argon2id + 会话 + CSRF + **OIDC/PKCE** + 等时反枚举 + 按来源登录限流 | 口令 + 会话 + CSRF HMAC；含 `unlock` 端点（账号维度） | **领先** |

### 2.5 前端、交付与文档

| 能力 | 本项目 | 参考项目 | 判定 |
|---|---|---|---|
| 控制台 | Vue 3 源码在库内；组件交互测试（用例数随提交变化） | 仅重建产物，无前端源码 | **领先** |
| 控制台结构 | `App.vue` 单文件承载多视图、无路由/状态库、单 bundle 无分割、仅亮色 | 未知（无源码） | 自身维护短板，不作对比判定 |
| 后端能力暴露度 | `supports_responses` 已在 `OperationsPane.vue` 可编辑；`extra_headers` 等能力仍需检查 UI 覆盖 | 恢复契约有较完整的管理端点 | 部分 |
| 前端门禁 | **无 lint/format**；Vitest 有；无浏览器 e2e 进 CI（`docs/e2e/*.png` 为人工截图） | 无（重建仓库无前端） | 落后（自身短板） |
| CI | 单 workflow 单 job：gofmt→vet→build→`go test -race`→前端 build+test，矩阵 PG 14/16，`--strict` | 无 CI（逆向工程仓库） | **领先** |
| 发布流程 | 无 tag、无 CHANGELOG、无镜像构建 job | 无 | 持平（双方都缺） |
| 接口契约文档 | README 只有路径 + 一句话，无字段表/错误码表/配置全表 | `docs/api-contract.zh-CN.md` 105 端点分组 + 参数/语义 + e2e 报告 | **落后** |

---

## 3. 劣势清单（按优先级）

### 候选工程缺口（优先级需与协议保真任务共同评估）

1. **准入只拒绝、不排队。** `internal/policy/policy.go` 的 `Acquire` 在预算/RPM/TPM/并发任一超限时立刻返回
   `ErrRateLimited` / `ErrBudgetExceeded`；`internal/policy/counters.go` 一旦 `active >= limit` 即拒。
   参考项目用 `Limiter.Acquire(ctx, globalMax, queueSize, queueTimeoutSeconds, keys)`
   （`reconstruction/internal/gateway/limiter.go`）做有界等待，默认 10 槽 / 3 秒，超时才 503 `gateway_overloaded`。
   **影响**：突发流量下拒单率与客户端重试风暴；且本项目连"当前在飞/排队"都没有对外视图（参考项目 overview 报
   `concurrency`、`waitQueue`、`streamingRequests`、`activeUsers`）。
2. **选号无负载感知。** `internal/gateway/gateway.go` 的 `resolveRoutes` 只按 `route_entries.priority` 取第一个健康路由；
   参考项目 `scheduler.go` 的 `leastUtilizedLocked` 先比在飞请求数、再比 priority、并列时组内轮转，并有
   `claimProbeLocked` 探测锁与按 API Key 的 `affinity`。**影响**：多上游账号时负载不均、慢账号被持续压。
3. **日志无保留期清理。** 仓库内没有任何删除 `requests` / `attempts` / `usage_ledger` / `audit_log` / `content_log`
   的路径，而 `.env.example` 暴露的 `GATEWAY_LOG_RETENTION_DAYS` 在 `internal/config/config.go` 里自己标注为
   informational（死旋钮）。参考项目由 `log.call_retention_days` 驱动周期清理。
   **影响**：长期运行库必然膨胀，且"可配置项"对运维撒谎。
4. **配置不能热改。** 内容日志开关/上限、会话 TTL、保留天数、OIDC、`GATEWAY_ALLOW_PRIVATE_UPSTREAMS` 全为启动期 env；
   参考项目 `runtimeconfig` 把 9+ 项做成请求级热读，并在写入事务提交后触发热重载。
   **影响**：每次变更都要重启，与"单实例高可用"的运维预期冲突。
5. **无运维 CLI。** `backend/cmd/` 下只有 `server`，无子命令/参数；管理员口令恢复只能走管理 API 或直接改库。
   参考项目有 `-reset-admin-password` 等标志。**影响**：账号锁死场景缺少安全的重置通道。

### P1 — 产品能力缺口（竞争力）

6. **智能路由缺失**：无 `auto` 模型名、无复杂度分类、无"简单/复杂模型组"回退，也没有决策日志与日统计。
   这是参考项目最显性的成本卖点（188 条默认样本、阈值 0.72、预览端点）。
7. **内容合规缺失**：三级决策（`allow`/`audit_only`/`block`）+ 关键词/语义两级匹配 + 审计与管理端点 + 独立四表。
   企业与教育场景常把它当准入项。
8. **统计层太薄**：只有 `StatsSummary`；缺六维聚合与时间序列趋势（参考项目 `usage-statistics` 与 `token-trend`
   带 `timezoneOffsetMinutes` 对齐与空桶补零）。控制台仪表盘因此只有单页汇总。
9. **请求体审计不产品化**：本项目只有"默认关闭的加密内容日志 + 单条读取"；缺开关/上限/保留期/导出/访问事件。
10. **跨协议转换窄**：只覆盖 Messages↔Chat 且限纯文本，无 Gemini 方向，也没有"同协议原生候选优先"的显式排序与开关
    （本项目目前靠 priority 顺序天然决定；一旦引入转换矩阵必须显式化，否则会把可透传的候选排在需要转换的候选之后）。
11. **无删除资源标签**：参考项目 `deleted_resource_labels` 保证删除资源后列表仍可读（回填 `deleted:true` 与空名）；
    本项目 DELETE 覆盖不全，且删除后历史 JOIN 会丢名字。
12. **控制台仍有能力缺口**：`supports_responses` 在 `5cfb93a` 中已通过供应商表单发送并显示（有前端测试），
    不再算缺口；`extra_headers`、`unpriced`/`zero_credit` 计数与自助侧部分操作仍需逐项验证和补全。
13. **无向量服务/向量任务**：既是独立能力缺口，也是第 6、7 条语义层的前置。

### P2 — 工程质量、前端与文档

14. **前端结构**：`frontend/src/App.vue` 854 行单文件承载登录页 + 13 个视图分支 + 表单/表格/弹窗，靠 `view` ref + `v-if`；
    无 Vue Router、无状态库、无代码分割、仅亮色主题；还留有不可达分支（providers/routing 的旧表单，已被 `OperationsPane` 取代）。
15. **前端无 lint/format 门禁**：`Makefile` 与 `scripts/test.sh` 只有 gofmt/vet；仓库无 eslint/prettier/editorconfig
    （遗留如 `styles.css` 的重复注释行）。
16. **无浏览器 e2e 进门禁**：`scripts/e2e/`（Python，无三方依赖）与 24 张人工截图都不在 `scripts/test.sh` 内，回归靠人。
17. **文档欠账**：README 无字段表/错误码表/配置全表（`GATEWAY_LOG_RETENTION_DAYS` 甚至没出现在 README）；
    无升级/回滚/排障章节；`docs/ACCEPTANCE_GAPS.md` 自认 §2–§8 的 `file:line` 多数未按当前工作树复测；
    `backend/go.mod` 把实际直接依赖 `coreos/go-oidc`、`golang.org/x/oauth2` 标成 `// indirect`（`go mod tidy` 未跑过）。
18. **CI 只覆盖验证**：无镜像构建/推送、无发布 tag/CHANGELOG；README 与 `Makefile` 关于 `make build` 是否先构建前端
    互相矛盾（`Makefile` 的 `build` 不依赖 `webui`，干净 clone 上只能得到占位页）。
19. **只支持 PostgreSQL**（参考项目 SQLite 零依赖起步）。**属策略选择而非缺陷**：本项目的 PG 迁移用
    `pg_advisory_xact_lock`、CI 跑 14/16 双版本矩阵，这是资产；若要降低试用门槛，应做"一键 compose + 文档"而不是换库。

---

## 4. 值得学的工程手法（与产品功能解耦）

| 手法 | 参考项目证据 | 落到本项目怎么做 |
|---|---|---|
| 运行期配置存储 + 事务 + 写后重载 | `reconstruction/internal/runtimeconfig/writer.go`（`BeginTx→change→Commit`）、`gateway/settings.go`（单事务 UPSERT 9 键）、请求级热读 | 新增 `settings` 表与 `internal/runtimeconfig`；env 只作**首次默认值**，保留全部 env 键名以兼容现有部署与文档 |
| 有界异步队列的"丢弃可见" | `recordqueue/queue.go`：`Enqueue` 非阻塞、满即丢弃并累加 `dropped`，`SetDropHandler` 上报 | 本项目 `internal/background/writer.go` 已有有界队列，补 `Dropped()` 计数并暴露到 `/api/admin/runtime` |
| 探测证据签名 | `provider/discovery.go` 的 `issueEvidence`/`validEvidence`：`<unix>.<base64url HMAC-SHA256>`，容忍 5 分钟时钟偏差 | 给本项目的 provider 探测结果加签名，使"模型清单来自何时、由谁探测"可审计（控制台可显示探测时刻） |
| 访问事件审计（读也审计） | bodyaudit 的 `view`/`export` 事件 | 本项目只有"内容日志读取"审计；把管理面**导出/下载**类读操作也纳入 `audit_log` |
| 时间对齐的稠密序列 | `token-trend` 的 `range` + `timezoneOffsetMinutes`，空桶补零 | 统计端点统一接受 `from`/`to`/`bucket`/`tz_offset_minutes`，返回稠密序列，前端渲染趋势不引新依赖（SVG） |
| 迁移幂等的双保险 | 单事务 + `migrations(id PRIMARY KEY)` 判重 + `CREATE TABLE IF NOT EXISTS` | 本项目已有 advisory lock + 版本表，但**无 checksum**；补一列 checksum 可在"手工回退/改历史文件"时显式失败，而不是静默跳过 |
| 构建标签控制可选能力 | `//go:build embed_webui` + 无标签返回 503 `webui_unavailable` | 本项目用无条件 `//go:embed` + 占位页；两者都能交付，但**测试与嵌入状态解耦**这条已修过一次（`ui_test.go` 用 `fstest.MapFS`），保持该约定即可 |
| 运维 CLI 入口 | `cmd/ai-gateway -reset-admin-password` | 在 `cmd/server` 加 `reset-admin-password`（`os.Args` 手写，不引 flag 库），复用现有 Argon2id 与 `store.Users` 接口 |
| 接口契约文档 | `docs/api-contract.zh-CN.md` 105 端点分组 + 参数语义 | 产出 `docs/api-contract.md`：端点 + 请求/响应字段表 + 错误码表 + 生效时机；直接由 `httpapi` 路由注册表逐条对照生成，避免再出现"README 承诺、代码没有" |
| 端到端脚本可复现 | 根 `scripts/pull-export.sh`、`analyze.sh`、`e2e-upstream-endpoint.sh`（自建实例 + 登录 + 注册上游 + 真实透传） | 本项目 `scripts/e2e/` 已具备同类能力；把它接进 CI 的一个 `e2e` job（起 PG + mock 上游）即可 |

---

## 5. 本项目已领先、不要照搬的部分

1. **两本账 + 预算硬拒绝**：参考项目没有价格、成本、账本与预算概念，只有 token 统计。不要为了"功能对齐"引入任何削弱
   `unpriced`/`zero_credit` 可见性或让积分对成本做换算的设计（`README.md` 的 credits 约定已显式禁止）。
2. **结算幂等 + 有界重试**：参考项目是"每次请求结束异步写一次"，本项目是幂等 ID + 重试 + 关闭排空。保留。
3. **PostgreSQL + 双版本 CI 矩阵**：不要为了"零依赖试用"换 SQLite；试用门槛用 compose 一键 + 文档解决。
4. **OIDC(PKCE) 与反枚举等时**：参考项目只有口令 + CSRF，无 OIDC、无时序加固。保留并继续扩（账号维度锁定可借鉴其 `unlock` 端点语义）。
5. **SSRF 感知传输层**：本项目 `internal/upstream/client.go` 在 dial 时固定 IP、禁重定向/私网/URL 凭据；参考项目只在模型发现时禁重定向。
6. **统一错误信封带 `request_id`**：保留为全仓唯一信封（含流内事件），新增端点必须复用。
7. **前端源码 + 组件交互测试在库内**：参考项目重建仓库连前端源码都没有。补 lint 与浏览器 e2e，但不要退回"只嵌产物"。
8. **显式非目标**（多实例共享协调、Prometheus/OTel、Webhook）不必因对标而改变；若要改，先改文档再改代码。

---

## 6. 落地路线图（建议顺序）

每项列出改动点与验收断言；以下是调研形成的**候选 backlog**，并非承诺一次迭代全部交付。
先以协议保真（Chat SSE reasoning/tool/usage、Responses 明确 501）和脱敏的 mock 上游对照测试作为本轮门禁；
之后按实际流量与风险决定 W1–W17 的顺序，每项独立评审与提交，不削弱账务、SSRF 或认证边界。

### 第一批（候选工程项；须在协议保真切片后重新排序）

| # | 工作项 | 改动点 | 验收断言 |
|---|---|---|---|
| W1 | 有界等待室 | `internal/policy/counters.go` 增加等待队列；`policy.go` 的 `Acquire` 支持 `WaitBudget`；env/`settings` 控制槽位与超时 | 并发上限 3、20 并发请求、等待窗口 200ms：峰值并发 ≤3，未超时者全部 200，超时者仍 429 且错误码不变 |
| W2 | 在飞/排队可观测 | `policy` 暴露 `(active, waiters)`；`GET /api/admin/runtime` 增加 `concurrency`/`wait_queue` | 计数器与实际在飞一致（并发测试断言） |
| W3 | 日志保留清理 | `internal/background` 复用维护 goroutine；新增按 `GATEWAY_LOG_RETENTION_DAYS` 清理 `requests`/`attempts`/`usage_ledger`/`content_log`；逐表计数进 runtime | 插入 3 天前记录 + 保留 1 天 → 清理后消失且计数 +N；保留 0 = 关闭清理并显式记录 |
| W4 | 选号调度 | `internal/gateway` 的 `routeStates` 扩为「在飞 + 冷却 + 最近成功」；`resolveRoutes` 排序改为「健康 → 在飞最少 → priority → 轮转」 | 2 个账号 + 20 个并发长请求：在飞分布差 ≤1；冷却期内不被选中；冷却到期探测成功后回到候选 |
| W5 | 运维 CLI | `cmd/server` 增加 `reset-admin-password` 子命令（`os.Args` 手写） | 忘记口令时可在不重启服务的前提下重置并登录成功；无参数时行为与现在完全一致 |

### 第二批（P1 前半，中等规模）

| # | 工作项 | 改动点 | 验收断言 |
|---|---|---|---|
| W6 | 运行期配置 | 新迁移 `settings` 表 + `internal/runtimeconfig`；先迁 4 项（内容日志开关/上限、日志保留天数、并发/排队参数），env 作默认 | PUT 配置 → 无需重启生效；重启后仍保持（库为准）；env 仍可作首次默认 |
| W7 | 统计层 | `store/queries.go` 增加六维聚合与时间序列；`GET /api/admin/stats/dimensions`、`/stats/trend?from&to&bucket&tz_offset_minutes` | 空桶补零、时区对齐；同一 `from/to` 两次调用结果稳定；`from>to` 返回 400 |
| W8 | 供应商预设与认证注入 | `providers` 增加预设枚举与 `auth_mode`（`none`/`bearer`/`x-api-key`/`x-goog-api-key`），替代让用户手写 `extra_headers` 传凭据；前端下拉选择；补图像/嵌入类预设 | 新建 Gemini 供应商不手填 header 即可探测成功；旧 `extra_headers` 继续生效（向后兼容） |
| W9 | 转换矩阵显式化 | `internal/adapter` 引入协议能力矩阵 + `conversion_required` 标记；`resolveRoutes` 保证原生候选恒优先；开关默认关 | 同时存在 Chat→OpenAI 与 Chat→Anthropic 候选时优先 OpenAI；开关关闭时行为与今天逐字节一致 |
| W10 | 软删除标签 | 新迁移 `deleted_resource_labels`；providers/aliases/model-groups/user-groups 删除时写标签，列表回填 `deleted:true` | 删除后列表仍返回该行且名字为空、标记为已删；历史请求详情不因删除而丢名 |

### 第三批（P1 后半：先做无向量的最小可用版）

| # | 工作项 | 改动点 | 验收断言 |
|---|---|---|---|
| W11 | 智能路由（无向量） | 新包 `internal/smartrouting`：文本特征加权打分 → 阈值 → 简单/复杂模型组；`auto` 模型名生效；决策日志 + 日统计 + 预览端点 | 短问答落到简单组、含代码块的长指令落到复杂组；无关模型名不走智能路由；阈值调整即时生效（配合 W6） |
| W12 | 内容合规（关键词层） | 新包 `internal/contentcompliance`：三级决策 + 审计表 + 管理端点；`block` 时 403 `content_policy_violation` 且不转发上游 | 命中 `block` 不产生上游 attempt；`audit_only` 放行且落审计；未启用时行为与今天一致 |
| W13 | 请求体审计产品化 | 在现有加密内容日志上加：开关/上限/保留期/导出/`view`+`export` 访问事件 | 超上限按上限截断并记摘要；导出写访问事件；保留期由 W3 的清理覆盖 |
| W14 | 向量服务与任务 | 新包 `internal/vectorservice` + `internal/vectortask`（有界并发、取消、剪枝）；配置校验与连通性测试 | 未配置时智能路由/内容合规的语义分支自动降级且不报错；任务可取消且剪枝后查询返回明确错误 |

### 第四批（P2，工程质量）

| # | 工作项 | 改动点 | 验收断言 |
|---|---|---|---|
| W15 | 前端拆分与门禁 | `App.vue` 拆成视图组件 + 轻量视图切换；引入 eslint（或 oxlint）+ prettier 进 `scripts/test.sh`；补 `view` 不可达分支清理 | 门禁在干净 clone 上幂等通过；`make build` 与 README 描述一致（二选一改） |
| W16 | 浏览器 e2e | 用无头浏览器跑「登录 → 建 Key → 数据面调用 → 看请求详情」最小链路，作为 CI 的独立 job | 链路失败即 CI 红；不依赖人工截图 |
| W17 | 发布与文档 | `docs/api-contract.md`（端点/字段/错误码）、`docs/CONFIGURATION.md`（全部 env + 默认值 + 生效时机）、CHANGELOG + 镜像构建 job；`go mod tidy` 修正 indirect | 文档里每个 env 都能被 grep 到代码引用；不存在的配置项一律删除（先处理 `GATEWAY_LOG_RETENTION_DAYS`） |

---

## 7. 边界与未确认

- 初稿来自独立的只读对标分析；本文随后依据 `5cfb93a` 和前端测试更新 Responses 基线，不能当作持续同步的验收报告。
- `supports_responses` 默认 `false`，未启用时无上游尝试并返回 501 `responses_unsupported`；不提供 Responses→Chat 回退。
- 旧 PI 流式 Chat 的 11 次上游 400 缺少匹配请求的脱敏校验响应，根因未确定；此文路线图不能代替该证据。
- 参考项目的端点与参数为逆向恢复结果，个别语义（如排队超时是否计次、异步队列是否有批量写入）在干净室实现中**未找到**
  对应字段，本文不把它当作产品行为断言。
- 未纳入本次对比：参考项目的逆向工程方法论（Podman digest 校验、DWARF 恢复、证据等级体系）与本项目无关，不构成优劣；
  其根 `scripts/*.sh` 的**可复现脚本**思路已作为工程手法记录在 §4。

---

## 附录 A：端点级对照清单

口径：参考端点为「方法 + 路径」对，数据取自 `docs/api-contract.zh-CN.md` 的表格（该文档自称 105 个端点）；
下文本项目一列只写与参考不同名的路径，同名省略。判定：**等价** / **部分**（有实现但语义或粒度不同）/ **缺口**。

### A.1 公共与数据面（8 个端点；Responses 能力取决于上游配置）

| 参考端点 | 本项目 | 判定 |
|---|---|---|
| `GET /health/live`、`GET /health/ready` | `/healthz`、`/readyz`（`httpapi/handler.go`） | 等价（路径命名不同，均含 DB 就绪） |
| `GET /v1/models` | 同名（按 Key 白名单过滤） | 等价 |
| `POST /v1/chat/completions`、`/v1/messages`、`/v1/embeddings`、`/v1/images/generations`、`/v1/responses` | 同名 | 前四项有对应接口；Responses 仅在供应商显式 opt-in 时转发，否则 501、无上游尝试 |

### A.2 身份与用户控制台（14 个端点 → 等价 10、缺口 4）

| 参考端点 | 本项目 | 判定 | 路线图 |
|---|---|---|---|
| `POST /auth/login`、`/auth/logout`、`/auth/password`、`GET /auth/current` | `/api/auth/login`、`/api/auth/logout`、`/api/self/password`、`/api/auth/me` | 等价（自助改密另撤销其它会话，多于参考） | — |
| `GET /console/api-keys`、`POST /console/api-keys`、`PATCH|DELETE /console/api-keys/{id}` | `/api/self/api-keys` 系列 | 等价 | — |
| `POST /console/api-keys/{id}/enable`、`/disable` | `PATCH /api/self/api-keys/{id}` 的 `active` | 等价（少一个动作端点） | — |
| `GET /auth/csrf` | 无独立端点：CSRF token 随登录/`me` 返回并存会话表 | 缺口（前端不需要，可不补） | — |
| `GET /console/models` | 无用户侧模型目录端点（`/v1/models` 是数据面 Key 维度） | 缺口（小） | — |
| `GET /console/usage` | 只有 `/api/self/requests`（记录级列表，无聚合） | 缺口 | W7 |
| `GET /console/usage/api-keys` | 无 | 缺口 | W7 |

### A.3 概览、用户与分组（21 个端点 → 等价 13、部分 4、缺口 4）

| 参考端点 | 本项目 | 判定 | 路线图 |
|---|---|---|---|
| `GET|POST /admin/users`、`POST /users/{id}/enable|disable|reset-password` | `/api/admin/users`（列表/创建，`PATCH` 改密与 `active`） | 等价 | — |
| `PUT|DELETE /admin/users/{id}` | 只有 `PATCH`（无删除用户） | 部分 | W10（若要软删除） |
| `GET|POST /admin/user-groups`、`PUT|DELETE /user-groups/{id}` | `/api/admin/user-groups`（`PATCH` 代 `PUT`） | 等价 | — |
| `GET|POST /admin/model-groups`、`PUT|DELETE /model-groups/{id}` | `/api/admin/model-groups` | 等价 | — |
| `GET /admin/overview/runtime` | `/api/admin/runtime`（仅计数器：队列/结算，无调度器实时视图） | 部分 | W2 |
| `GET /admin/upstream-model-options?protocolType=` | `GET /api/admin/providers/{id}/models`（按供应商而非按协议类型） | 部分 | — |
| `POST /admin/users/{id}/unlock` | 无（只有按来源登录限流，无账号维度锁定） | 缺口 | — |
| `GET /admin/overview/token-trend` | 无时间序列端点 | 缺口 | W7 |
| `GET /admin/user-groups/options`、`GET /admin/model-groups/options` | 无辅助端点（前端直接拉列表） | 缺口（小） | — |

### A.4 上游账号（9 个端点 → 等价 7、部分 2）

| 参考端点 | 本项目 | 判定 | 路线图 |
|---|---|---|---|
| `GET /admin/upstream-providers`、`GET|POST /admin/upstream-accounts`、`PUT|DELETE /upstream-accounts/{id}`、`POST .../enable|disable` | `/api/admin/providers` 系列（`PATCH` 代 `PUT`，`active` 代 enable/disable） | 等价 | — |
| `POST /admin/upstream-accounts/{id}/discover-models` | `POST /api/admin/providers/{id}/discover`（另有 `/verify` 探针） | 部分（缺 `evidence` 签名与 `added/unchanged/missing` 三态） | — |
| `POST /admin/upstream-accounts/discover-models`（集合级） | 无集合级 discover | 部分 | — |

### A.5 智能路由（14 个端点 → 全部缺口）

`policy`(GET/PUT)、`samples`(GET/POST)、`samples/bulk`、`samples/{id}`(PUT/DELETE)、`vector-tasks`(POST)、
`vector-tasks/current`、`vector-tasks/{task}`、`vector-tasks/{task}/cancel`、`preview`、`decisions`、`statistics`，
外加 `GET /admin/smart-routing` 别名 —— **本项目 0 个等价端点**，见 §3 第 6 条、路线图 W11/W14。

### A.6 内容合规（21 个端点 → 全部缺口）

`settings`(GET/PUT)、`policies`(GET/POST)、`policies/{id}`(PUT/DELETE)、`words`(GET/POST)、`words/{id}`(PUT/DELETE)、
`words/import`、`samples`(GET/POST)、`samples/{id}`(PUT/DELETE)、`vector-tasks` 四端点、`audits`(GET)、`audits/{id}` ——
**本项目 0 个等价端点**（`block`/`audit_only` 语义、归一化、审计表全缺），见 §3 第 7 条、路线图 W12。

### A.7 日志、统计与设置（14 个端点 → 等价 2、部分 1、缺口 11）

| 参考端点 | 本项目 | 判定 | 路线图 |
|---|---|---|---|
| `GET /admin/call-logs`、`GET /admin/call-logs/{id}` | `/api/admin/requests`、`/requests/{id}`（返回 attempts + ledger，多于参考） | 等价 | — |
| `GET /admin/body-audits/{requestId}` | `/api/admin/content/{request-id}`（加密内容日志，默认关） | 部分 | W13 |
| `GET /admin/call-log-settings` | 无（保留期只在 config，且不生效） | 缺口 | W3、W6 |
| `GET /admin/usage-statistics` | 无六维聚合 | 缺口 | W7 |
| `GET|PUT /admin/gateway-settings` | 无（全 env） | 缺口 | W6 |
| `GET|PUT /admin/vector-service`、`POST /admin/vector-service/test` | 无 | 缺口 | W14 |
| `GET|PUT /admin/body-audit-settings`、`POST /admin/body-audit-test`、`POST /admin/body-audits/{requestId}/export` | 无 | 缺口 | W13 |

### A.8 小结

| 分组 | 参考端点数 | 等价 | 部分 | 缺口 |
|---|---|---|---|---|
| 公共与数据面 | 8 | 8（路径口径） | 0 | 0 |
| 身份与用户控制台 | 14 | 10 | 0 | 4 |
| 概览、用户与分组 | 21 | 13 | 4 | 4 |
| 上游账号 | 9 | 7 | 2 | 0 |
| 智能路由 | 14 | 0 | 0 | 14 |
| 内容合规 | 21 | 0 | 0 | 21 |
| 日志、统计与设置 | 14 | 2 | 1 | 11 |
| **合计** | **101** | **40** | **7** | **54** |

计数口径说明：本文按「方法 + 路径」对逐条统计为 101，契约文档自称 105，差额来自别名端点（如 `GET /admin/smart-routing`、
`/admin/settings/vector-service`）与文档正文补充的个别条目；口径差异不影响结论 —— **缺口集中在智能路由（14）、内容合规（21）
与设置/统计/审计（11）三处，合计 46 个端点，占缺口总数的 85%**。

### A.9 非端点差异（契约之外）

| 参考项目 | 本项目 | 判定 | 路线图 |
|---|---|---|---|
| 软删除标签 `deleted_resource_labels`（列表回填 `deleted:true`、`name` 为空） | 无 | 缺口 | W10 |
| 运行期写入失败返回 503 `runtime_reload_failed` | 无热重载概念 | 缺口 | W6 |
| CLI `ai-gateway reset-admin-password --config` | 无 CLI | 缺口 | W5 |
| 本地化响应头 `Content-Language` + `Vary` | 无（前端有中文错误映射表，API 错误体英文） | 缺口（价值有限） | — |
| 错误形状 `{code,message}` | `{error:{message,type,code,request_id}}`（OpenAI 风格，体内带 request_id） | **领先** | — |
| 内嵌 WebUI 构建标签 `embed_webui` / 503 `webui_unavailable` | `//go:embed all:static` + 占位页回退 | 等价（取向不同） | — |
| `X-Request-ID` 32 位十六进制 | 调用方提供或生成 + 独立 `X-Gateway-Request-ID` 落库键 | 等价/领先 | — |

---

## 附录 B：本次修订记录

- **修正 §2.1 两行**：`Retry-After` 经代码核对**已支持秒数与 HTTP-date**（`internal/adapter/adapter.go` 的
  `ParseRetryAfter`，上限 5 分钟），判定由「落后」改为「持平」；请求头处理上本项目**不转发任何客户端头**
  （每个上游请求重建，只带 Content-Type/Accept/凭据/`extra_headers`），判定由「落后」改为「**领先**」。
  据此把原 W8（敏感头剥离 + `Retry-After`）替换为「供应商预设与认证注入」。
- **补录附录 A**：端点级对照，含小结与计数口径说明。
- 其余断言经复核成立：`GATEWAY_LOG_RETENTION_DAYS` 仅在 `internal/config/config.go` 出现且注释自称 informational
  （默认 0 = 永久保留）；`extra_headers` 前端 0 引用；`Makefile` 的 `build` 不依赖 `webui`（与 `README.md:109` 矛盾）；
  `backend/go.mod` 把 `coreos/go-oidc`、`golang.org/x/oauth2` 标为 `// indirect`；`frontend/package.json` 无 lint 脚本。
