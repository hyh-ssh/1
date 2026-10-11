# V1 验收核对与差距清单

核对对象：`docs/PROJECT_ANALYSIS.md` 第 11 节「V1 验收标准建议」、第 12 节「已确定的 V1 运行决策」、第 8.3 节遗留点。

核对基线：分支 `main`，提交 `92b2970`（其上为 Phase 0–5 的 `610acd0` 基线，`92b2970` 为初始化增量：Makefile、前端 Vitest、真实控制台嵌入、迁移加载器加固）。核对方式为只读代码审阅 + 本地实跑 + 容器实跑，证据均给出 `file:line`。**行号说明**：`92b2970` 之后工作树已前进 140+ 个文件，§2–§8 的 `file:line` 原本是当时的记录；本次（`56a78a8`、`a0bdf4e`、`a666f00` 及随后的前端/文档提交）已按当前工作树逐条复测本次改动涉及文件的引用（`httpapi/auth.go`、`httpapi/admin.go`、`httpapi/groups.go`、`httpapi/self.go`、`httpapi/login_limit.go`、`store/users.go`、`store/sessions_test.go`、`frontend/src/App.vue`、`api.ts`、`errors.ts`、`errors.spec.ts`、`MemberConsole.spec.ts`），其余引用尚未整体复测（见 §12.4）。标为「原始描述位置／原始缺口描述」的引用是历史定位，刻意保留原值。本文件只记录**已核对**的事实与缺口，未核对的能力不在此列。

## 1. 结论摘要

| 验收分组 | 条目 | ✅ | ⚠️ | ❌ |
|---|---|---|---|---|
| 网关能力 | 7 | 7 | 0 | 0 |
| 管理能力 | 5 | 5 | 0 | 0 |
| 统计与计费 | 6 | 6 | 0 | 0 |
| 安全与运维 | 8 | 8 | 0 | 0 |
| 合计 | 26 | 26 | 0 | 0 |

第 12 节运行决策 9 条现已全部成立（决策 5 的措辞已按 G2 修正）。第 8.3 节 4 个遗留点中 3 个已解决（Phase 3，`3957675`）、1 个部分解决——其中「多实例策略」一项对应的 G11 已按方案补齐可替换接缝（Phase 5，`7c9a377`），分布式实现仍为明确的 V1 非目标。

**没有任何验收项判定为未实现。** 差距集中在契约细节、前端深度、测试覆盖与运维前置条件。

## 1.1 修复进度（活账）

| 阶段 | 范围 | 提交 | 状态 |
|---|---|---|---|
| Phase 0 | 验证门禁可复现（G12 前置） | `8c4d7ec` | ✅ 完成 |
| Phase 1 | P0 结算完整性（G1、G2） | `08df9e9` | ✅ 完成 |
| Phase 2 | P1 契约与健壮性（G3–G6） | `4a06721` | ✅ 完成 |
| Phase 3 | Responses 归一化（G7） | `3957675` | ✅ 完成 |
| Phase 4 | 前端深度（G8、G9） | `49fcdd4` | ✅ 完成 |
| Phase 5 | 测试加固与接缝（G10、G11、G14） | `7c9a377` | ✅ 完成 |

验证门禁：`scripts/test.sh [--strict]`（gofmt → vet → build → `go test -race` → `npm ci && npm run build && npm run test`），`CI=true` 等价 `--strict`。`.github/workflows/ci.yml` 在 `postgres: ["14","16"]` 矩阵（`fail-fast: false`）上以 `--strict` 跑同一脚本；本机已用本地 Podman 拉取同款 `postgres:14` / `postgres:16` 镜像分别实跑，两条腿均 `All checks passed.`（详见 §11）。`make test` 为目标封装；`make webui` 先构建 Vue 控制台再嵌入。

## 2. 网关能力（7/7 ✅）

| 序号 | 验收项 | 判定 | 证据 | 缺口 |
|---|---|---|---|---|
| 1 | OpenAI SDK 调用 Chat Completions | ✅ | 路由 `internal/httpapi/auth.go:104`；解码 `httpapi/gateway.go:135,241-307`；响应映射 `gateway.go:329-343` | — |
| 2 | 非流式与流式文本 | ✅ | 非流式 `httpapi/gateway.go:158`；流式 `gateway.go:154-156,170-239`；上游 SSE `internal/adapter/sse.go:29,146-188`；编排 `internal/gateway/gateway.go:207-310` | — |
| 3 | Tool Calling | ✅ | 请求透传 `adapter/openai.go:264-269,364-380`；响应解析 `openai.go:494-498`；流式增量 `adapter/sse.go:161-167`；测试 `adapter/sse_test.go:19,42-46` | — |
| 4 | 至少 OpenAI-compatible | ✅ | 适配器选择 `gateway/gateway.go:592-598`；实现 `adapter/openai.go:26-34`；另支持 `anthropic`/`gemini`（`model/entities.go:45-46`） | — |
| 5 | 别名配置多个有序上游 | ✅ | 模型 `model/entities.go:91-106`；排序 `gateway/gateway.go:499-567`（priority 升序、ID 兜底）；管理 API `httpapi/admin.go:669-698,768-798` | — |
| 6 | 连接失败/超时/429/5xx 故障转移 | ✅ | 分类 `adapter/adapter.go:69-88`、`adapter/openai.go:74-81`；可重试判定 `adapter/adapter.go:60-67`；编排 `gateway/gateway.go:152-196,233-305`；冷却退避 `gateway/health.go:60-91`；测试 `gateway/gateway_test.go:73,118,140,167` | 流式首帧后停止故障转移（`gateway.go:298`）为设计约定，非缺陷；见 G5 |
| 7 | 稳定 OpenAI 风格错误结构 + Request ID | ✅ | 错误结构 `httpapi/handler.go:117-127`；映射 `httpapi/gateway.go:405-431`；Request ID 中间件 `handler.go:66-81,13,77` | 见 G3、G4 |

## 3. 管理能力（5/5 ✅，缺口在前端深度）

| 序号 | 验收项 | 判定 | 证据 | 缺口 |
|---|---|---|---|---|
| 1 | 管理员登录 | ✅ | `internal/httpapi/auth.go:108-110,155`（路由注册与 `login`）；登录审计 `auth.go:167` → `login_limit.go:81-94`；前端 `frontend/src/api.ts:40,45,50`、`App.vue:186-200,607` | — |
| 2 | 管理用户与 API Key | ✅ | `httpapi/admin.go:83,85`（路由分发）、`:118`（`users`）、`:240`（`apiKeys`）；前端 `App.vue:25,72,462,521,552,570` | 见 G8 |
| 3 | 配置 Provider/模型/别名/路由 | ✅ | `admin.go:87,89`（路由分发）、`:372`（`providers`）、`:630`（`aliases`）、`:812`（`routes`）；前端 `OperationsPane.vue:8-20,40-100`、`App.vue:149` | 见 G8、G9 |
| 4 | 配置价格/预算/限流 | ✅ | `admin.go:91,93,95`（路由分发）、`:859`（`prices`）、`:986`（`budgets`）、`:1143`（`rateLimits`）；前端 `App.vue:72-79,462,521,552,570` | 见 G8 |
| 5 | 查看请求明细/Attempt/费用/审计 | ✅ | `admin.go:97,99,101,103`（路由分发）、`:1308`（`requests`）、`:1359`（`audit`）、`:1388`（`stats`）、`:1413`（`content`）；前端 `OperationsPane.vue:13,87,100`、`App.vue:25` | 审计为只读列表 |

后端实际路由前缀为 `/api/admin/…`（挂载点 `auth.go:118-120`；分组路由与角色门禁 `groups.go:59`）。全部分组均有对应 Vue 入口，**不存在无前端入口的管理路由**。

## 4. 统计与计费（6/6 ✅）

| 序号 | 验收项 | 判定 | 证据 | 缺口 |
|---|---|---|---|---|
| 1 | 每个成功请求有 Request/Attempt/Ledger | ✅ | Request/Attempt `gateway/records.go`；Ledger `billing/billing.go`、`store/queries.go`；无价格时 `Result.Unpriced` → `attempt.settlement_error=unpriced` + `unpriced_settlements` 计数 | G1 已修复 `08df9e9`（口径：Ledger 行要求已配置价格版本） |
| 2 | 故障转移成本累加（Cost 每 attempt / Credits 仅成功） | ✅ | `gateway/records.go:158,304,278-312`；规则 `billing/billing.go:132-138` | G2 已修复 `08df9e9`（失败用量统一由 `failedAttemptUsage` 决定） |
| 3 | 历史账本绑定价格版本 | ✅ | `billing/billing.go:129`；表 `store/migrations/0001_init.sql:162-176`；版本表 `:78-88` 唯一且只 INSERT（`store/prices.go:13-22`） | — |
| 4 | 按时间/用户/模型/Provider 过滤 | ✅ | 参数解析 `httpapi/admin.go:1371-1430`；SQL `store/queries.go:26-58,171-203`；前端 `DashboardPane.vue:9-13,35` | — |
| 5 | 请求量/成功率/Token/Cost/Credits/延迟/TTFT | ✅ | `store/queries.go:204-218`；字段 `model/stats.go:24-45`；前端 `DashboardPane.vue:22` | TTFT 只统计流式成功请求（`queries.go:216-218`） |
| 6 | 预算/限流并发确定性 | ✅ | 全局锁 `internal/policy/counters.go:65,102-131`；按用户锁 `internal/policy/counters.go:188-198`（`policy/policy.go:70-77` 调用）；滚动窗口 `internal/policy/counters.go:212-228` | 见 G10 |

`docs/OPTIMIZATION_IMPLEMENTED.md` 声称的「分位数、路由表现、API 过滤」经核实全部存在：P50/95/99 `store/queries.go:213-218`；路由 P95 与 RouteStats `store/accounting_queries.go:30-35`；API 过滤 `store/queries.go:177-179`。原声称的「毛利」并非缺失，而是已按设计整体移除：`StatsSummary` 不再有 `profit` 字段，改为并列报告两条账本——上游成本 `Cost`（CNY，`model/stats.go:30`）与积分消耗 `Credits`（`model/stats.go:31`，均值 `AvgCreditsPerRequest` 见 `model/stats.go:45`）；汇总在 `store/queries.go:204-233` 构造，其中 `store/queries.go:227-229` 明确注明不再派生毛利。

## 5. 安全与运维（8/8 ✅）

| 序号 | 验收项 | 判定 | 证据 | 缺口 |
|---|---|---|---|---|
| 1 | 上游密钥不明文落库 | ✅ | AES-256-GCM `crypto/crypto.go:42-56`；加密写入 `httpapi/admin.go:399,408,559,610-617`；列 `migrations/0001_init.sql:43-44`；凭证头覆盖被拒 `admin.go:587` | — |
| 2 | 网关 API Key 不可恢复 | ✅ | SHA-256 `crypto/crypto.go:145-149`；只写 hash `admin.go:282`，明文仅创建时返回 `:289`；表 `migrations/0001_init.sql:26-31` | 无盐 SHA-256（不可逆，满足验收） |
| 3 | 内容日志默认关闭 | ✅ | 默认 false `config/config.go:31,60`；开关守卫 `gateway/gateway.go:97-104`、`gateway/content.go:13`；密文列 `migrations/0001_init.sql:196-200` | — |
| 4 | 管理操作有审计 | ✅ | `admin.go:1563`（`recordAudit`，含 `content/` 读取）；登录审计 `auth.go:167` → `login_limit.go:81-94` | 审计写入失败被忽略 |
| 5 | 迁移可重复安全执行 | ✅ | 事务 + `pg_advisory_xact_lock` + `schema_migrations` `store/store.go:72-127`；文件名过滤（跳过 `._*.sql` 等）`store/store.go:129-152`；双次 Migrate 集成测试 `store/groups_test.go:53,56`、`store/migrations_test.go:20` | 见 G6 |
| 6 | `go test ./...` 通过 | ✅ | 13 包全 `ok`（带 `TEST_DATABASE_URL` 实跑） | 见 G12 |
| 7 | Vue + Go 单一部署产物 | ✅ | `Dockerfile` 多阶段构建：`frontend-build` 产出 `frontend/dist` → `backend-build` `COPY --from=frontend-build /src/frontend/dist/ ./internal/httpapi/static/` → 运行时单一二进制；`//go:embed all:static` `httpapi/ui.go:17`。**已在本地 Podman 实跑镜构建与端到端**（真实控制台 `index.html` + `assets/index-*.js` 116 KB 由 8080 返回） | 见 G13（已闭环） |
| 8 | 健康检查与优雅关闭 | ✅ | `/healthz`、`/readyz` `auth.go:104-105`；`handler.go:42-64`；关闭 `cmd/server/main.go:35,120-138`；超时 `config/config.go:63` | — |

## 6. 第 12 节运行决策核对

| 决策 | 判定 | 证据 |
|---|---|---|
| 1 Chat + Responses；Responses 状态协议仅 OpenAI-compatible | ✅ | `model/entities.go:77`（`ResponsesState`）；`adapter/anthropic.go:19`、`adapter/gemini.go:22` 明确不支持 |
| 2 按接口类型选 Adapter，拒绝未知类型 | ✅ | `gateway/gateway.go:592-600` |
| 3 币种固定 CNY，金额用 decimal | ✅ | `model/money.go:7-10`；`model/entities.go:113-114` |
| 4 Budget 硬拒绝返回 429 | ✅ | `gateway/records.go:328-329`；`policy/policy.go:200-231`（`ErrBudgetExceeded` 于 `:226-230`） |
| 5 每个 attempt 的 Cost 都计入；Credits 仅最终成功 | ✅ | Cost 累加 `gateway/records.go`；Credits 门控 `billing/billing.go`；失败用量统一由 `failedAttemptUsage`（`gateway/records.go`）决定，非流式失败通常无上游用量故无 Cost，文档已按此精确化。见 G2（已修复 `08df9e9`） |
| 6 内容日志默认关闭、截断 + AES-GCM、仅管理员可解密、查看写审计 | ✅ | `config/config.go:60-61`；`gateway/content.go:34-44`；`httpapi/admin.go:1309-1337`；查看审计 `admin.go:58` |
| 7 多管理员共用 `admin` 角色，无细分 RBAC | ✅ | `model/entities.go:9-10`；无 RBAC 代码 |
| 8 单实例进程内协调 | ✅ | `policy/policy.go:28-31`；`internal/policy/counters.go:62-63` 注释与实现一致 |
| 9 不引入 Prometheus/OTel/Webhook | ✅ | 全仓库无相关引用 |

## 7. 第 8.3 节遗留点核对

| 遗留点 | 状态 | 证据 |
|---|---|---|
| 1 Responses `Input` 仍为 `json.RawMessage` 透传，无统一转换与校验 | ✅ 已解决（Phase 3，`3957675`） | 新增 `protocol.NormalizeResponsesInput`（`protocol/responses_input.go`）；`httpapi/responses.go` 在 decode 阶段归一化，结构非法时返回 400。原始描述位置： `protocol/types.go:101` |
| 2 Responses 函数调用参数增量保留在 `Raw`，未映射到统一 Tool Call 增量 | ✅ 已解决（Phase 3，`3957675`） | `adapter/sse.go` 将 `response.function_call_arguments.delta` 与 `response.output_item.added` 映射为 `protocol.ToolCallDelta`（`Index`/`ID`/`Name`/`Arguments`），原始事件仍保留在 `Raw`。原始描述位置： `protocol/types.go:92,95` |
| 3 `StreamEvent` 无 choice index / 响应 ID / 显式 Responses event type | ✅ 已解决（Phase 3，`3957675`） | `StreamEvent` 增加 `Index`、`ResponseID`、`Event`、`ToolCalls`；未知事件类型继续经 `Raw` 透传。原始描述位置： `protocol/types.go:89-96` |
| 4 供应商连接测试、流式断开专项测试、多实例策略 | 部分解决 | 连接验证已实现 `httpapi/admin.go:466-507`（`/providers/{id}/discover\|verify`）；流式断开测试已存在 `adapter/native_test.go:75`、`adapter/sse_test.go:124`、`gateway/native_test.go:99-131`；多实例策略仍未实现（见 G11） |

## 8. 差距清单

### P0 — 与验收条款直接冲突

- **G1 成功请求可能没有 Ledger 行。** ✅ **已修复（Phase 1，`08df9e9`）**：`billing.Result` 新增 `Unpriced`，`settleAttempt` 写 `settlement_error=unpriced` 并计入 `/api/admin/runtime` 的 `unpriced_settlements`；第 11 节措辞已改为「Ledger 行要求已配置价格版本」。原始缺口描述： 当命中无价格版本或 usage 计量项为空时，结算直接返回，不写任何账本（`internal/billing/billing.go:93,100-102`）。此时成功请求只有 Request/Attempt。影响第 11 节「每个成功请求有 Request、Attempt 和 Ledger 数据」，也与「（当时的措辞：Cost/Sale）可审计」的诉求冲突。
- **G2 非流式失败 attempt 的上游成本记为 0。** ✅ **已修复（Phase 1，`08df9e9`）**：抽出统一的 `failedAttemptUsage(observed, inputBytes, outputBytes)`，四处失败路径（Chat/Responses 非流式与流式、原生路径）共用「回报用量 → 采用；观察到部分输出 → 估算；两者皆无 → 无用量」，第 12 节决策 5 措辞已精确化。原始缺口描述： 非流式失败路径传空 usage（`internal/gateway/gateway.go:183`），只有流式失败路径携带 `attemptUsage`（`gateway.go:291`）。这与第 12 节决策 5「失败请求已产生的上游 Cost 保留」不一致——非流式场景下不保留。同时使第 11 节「多次故障转移的上游成本能够累加」在非流式路径上只在**成功** attempt 上成立。

### P1 — 契约与健壮性

- **G3 流式首帧后的错误事件缺 `code`。** ✅ **已修复（Phase 2，`4a06721`）**：新增 `errorBody`（`httpapi/handler.go`）统一错误信封，Chat SSE、Responses SSE 与原生 SSE 的流式错误事件均复用 `errorDetail` 形状并带 `code`。原始缺口描述： `internal/httpapi/gateway.go:198-199` 发出的 `{"error":{"message","type"}}` 与主错误结构（`httpapi/handler.go:117-127`，含 `code`）不一致，客户端需要两套解析。
- **G4 错误 body 不携带 request_id。** ✅ **已修复（Phase 2，`4a06721`）**：`errorDetail` 增加 `RequestID`，`writeError` 从响应头 `X-Request-ID` 回填（签名不变），流式错误事件从上下文取同一 ID。原始缺口描述： 仅通过响应头 `X-Request-ID` / `X-Gateway-Request-ID` 暴露（`httpapi/handler.go:13,77`）。与 OpenAI 行为一致，属可选增强，但会让"稳定错误结构 + Request ID"的验收复现成本变高。
- **G5 路由解析阶段的失败不降级。** ✅ **已修复（Phase 2，`4a06721`）**：`resolveRoutes` 遇到凭据解密或适配器构建失败时跳过该路由、写日志并累加 `route_resolve_failed`，仅当所有路由都解析失败时才返回解析错误（无坏配置时仍为 `ErrNoRoute`）。原始缺口描述： `resolveRoutes` 中 provider 凭据解密或适配器构建失败会整体返回，不尝试下一路由（`internal/gateway/gateway.go:553-559`）。单个配置损坏的 provider 会放大为整个别名的失败。
- **G6 迁移文件并非逐句幂等。** ✅ **已修复（Phase 2，`4a06721`）**：`0004_accounting.sql` 全部改为 `ADD COLUMN IF NOT EXISTS` / `CREATE ... IF NOT EXISTS` / `UPDATE ... WHERE settlement_id IS NULL`；新增 `TestMigrationsAreIndividuallyIdempotent`（临时 schema 中每个迁移文件连跑两次，并已用回退版本验证能捕获旧缺陷）。原始缺口描述： `internal/store/migrations/0004_accounting.sql:2-12` 使用裸 `ALTER TABLE ... ADD COLUMN`。安全性完全依赖 `schema_migrations` 版本表与事务（`store/store.go:78-132`）；版本表一旦丢失或手工回退，重跑会失败。
- **G7 Responses 统一转换仍缺失。** ✅ **已修复（Phase 3，`3957675`）**：新增 `protocol.NormalizeResponsesInput`（`input` 字符串或消息数组 → `[]Message`，结构非法时在 decode 阶段返回 400，`function_call` / `function_call_output` 归一化，未知条目保留在 `Input`）；`adapter/sse.go` 把 `response.function_call_arguments.delta` 与 `response.output_item.added` 的 function call 映射为带 `Index`/`ID`/`Name`/`Arguments` 的 `protocol.ToolCallDelta`；`StreamEvent` 增加 `Index`、`ResponseID`、`Event`、`ToolCalls`。原始缺口描述： `ResponsesRequest.Input` 为 `json.RawMessage`（`protocol/types.go:101`）；工具调用参数增量留在 `Raw`；`StreamEvent` 无 choice index / 响应 ID / 事件类型（`protocol/types.go:89-96`）。这三项使 Responses 路径无法像 Chat 路径那样做跨供应商统一编排。

### P2 — 前端深度、测试与运维前置

- **G8 管理前端只有「创建 + 列表」。** ✅ **已修复（Phase 4，`49fcdd4`）**：`App.vue` 增加按资源的能力表（users 可编辑/停用；keys、budgets、limits 可编辑/停用/删除；prices 保持仅创建）与编辑模式（走 `PATCH` 而非 `POST`，成功或取消后清理编辑态），破坏性操作一律 `window.confirm`；`OperationsPane.vue` 增加别名重命名/删除、供应商删除、路由移除确认与可保存的模型能力复选框。后端补齐 `RenameAlias`/`DeleteAlias`（重名 409 `alias_conflict`）、`DELETE /api/admin/aliases/{id}` → 204、别名 `PATCH` 接受 `{name?, active?}`，并修正 `SetAliasActive` 未检查影响行数、对不存在的 id 返回 200 的问题。**口径更正**：价格版本在后端同样没有编辑/删除（`/api/admin/prices/{id}` 的 `PATCH`/`DELETE` 实测返回 405），因为它是不变账本快照，故 UI 与之保持一致，不新增破坏性价格变更。原始缺口描述： 后端已具备但 UI 未暴露：用户禁用/改密（`admin.go:181`）、API Key 编辑与删除（`admin.go:302,333`）、预算/限流的编辑与删除（`admin.go:965,973,1120,1128`）、别名重命名与删除。别名在 UI 上只能启用/停用加路由增删。
- **G9 模型能力编辑 UI 提交空 capabilities。** ✅ **已修复（Phase 4，`49fcdd4`）**：`OperationsPane.vue` 为每个模型目录项渲染六个能力复选框（`streaming`/`multimodal`/`tool_calling`/`structured_output`/`reasoning`/`responses_state`，与 `model.Capabilities` 的 JSON tag 一致），`addModel` 提交真实 `capabilities`，新增 `保存能力` 通过 upsert 回写。原始缺口描述： 前端入口存在（`OperationsPane.vue`），但实际提交内容不含能力字段。
- **G10 预算/限流确定性缺少并发测试证明。** ✅ **已修复（Phase 5，`7c9a377`）**：新增 `internal/policy/policy_concurrency_test.go`（8 个用例）在 `-race` 下断言并发下预算不超发、限流计数精确，`internal/policy` 覆盖率 68.2% → 80.4%。原始缺口描述： 机制是进程内 `sync.Mutex` 加按用户锁（`policy/policy.go:30,34`），`go test -race ./...` 全绿，但仓库内没有针对预算/限流的并发压力用例，确定性目前只由代码推理支撑。
- **G11 多实例共享协调未实现。** ✅ **已按方案处置（Phase 5，`7c9a377`）**：新增 `internal/policy/counters.go`，把进程内策略状态收敛到导出的 `CounterStore` 接口（默认 `NewMemoryCounters()`），替换点为 `NewServiceWithCounters(store, counters)`；`PROJECT_ANALYSIS.md` 决策 8 与 `README.md` 已点名该接口作为唯一替换点。**按方案非目标，未实现分布式版本**，V1 单实例边界保持显式声明。原始缺口描述： 预算、RPM/TPM/并发、维护任务均为进程内（`policy/policy.go:158-159`）。文档已在 `PROJECT_ANALYSIS.md:651` 与 `README.md:110` 显式声明为 V1 外，属已知边界而非隐藏缺口。
- **G12 store 的 SQL 与聚合在默认测试中全部 SKIP。** ✅ **已修复（Phase 0，`8c4d7ec`；CI 矩阵于 `92b2970` 扩为 14+16）**：新增 `scripts/test.sh`（未设 `TEST_DATABASE_URL` 时醒目告警，`--strict`/`CI=true` 时硬失败）与 `.github/workflows/ci.yml`（`postgres: ["14","16"]` 矩阵跑同一脚本）。原始缺口描述： 未设置 `TEST_DATABASE_URL` 时 `store/groups_test.go:19-23` 直接跳过，覆盖率由 52.6% 掉到 6.1%。分位、过滤、聚合的正确性因此默认无回归保护，需要在 CI 提供 PostgreSQL。
- **G13 本地 `static/` 只有占位 `index.html`。** ✅ **已闭环（`92b2970`，容器实跑验证）**：`backend/internal/httpapi/static/` 现仅含被跟踪的 `.keep` 锚点，占位页移入 Go 源码常量 `fallbackIndexHTML`（`httpapi/ui.go:36-40`），由 `//go:embed all:static`（`ui.go:17`）嵌入；`make webui` / `make build` / Docker 多阶段构建会把真实 Vue 产物拷入 `static/`，此时服务真实控制台，纯本地 `go build` 未构建前端时回退占位页。**已在本机 Podman 实跑 `podman build --format docker --target backend` 并用 `postgres:16-alpine` 端到端验证**：`/` 返回真实控制台（标题 `LLM Gateway 管理控制台`、`/assets/index-BQG5W-qe.js` 116490 字节 `text/javascript`）、缺失资源 404、`/v1/models` 401、登录/`me`/`runtime` 200。原始描述：`backend/internal/httpapi/static/` 仅含回退页，真实 Vue 产物由 Docker 多阶段构建注入；本机无 Docker 故未实跑。**缺口已消除**：本地 Podman（libkrun 机器）已可实跑镜像构建。
- **G14 覆盖率偏低。** ✅ **已修复（Phase 5，`7c9a377`；数字于 `92b2970` 复测刷新）**：`cmd/server` 14.4% → 67.0%、`internal/model` 40.5% → 100.0%、`internal/httpapi` 46.6% → 70.3%、`internal/policy` 68.2% → 80.4%，全项目 约 54% → 67.7%（`-coverpkg=./...` 合并 profile，去重 block）。`internal/store` 54.6%（`TEST_DATABASE_URL` 就位时；缺失时 `scripts/test.sh` 显式告警）。原始缺口描述： 带 `TEST_DATABASE_URL` 实测：`cmd/server` 14.4%、`model` 40.5%、`httpapi` 46.6%、`store` 52.6%、`adapter` 57.3%、`gateway` 61.1%。偏高：`config` 100%、`background` 98.9%、`protocol` 97.5%、`crypto` 87.0%、`upstream` 84.1%、`billing` 81.7%。

### Phase 4 手工走查记录（G8/G9 验收）

用本地 PostgreSQL（库 `llm_gateway_ui`）+ `GATEWAY_LISTEN_ADDR=127.0.0.1:18093` 起真实服务，把 `frontend/dist` 拷入
`backend/internal/httpapi/static/` 后嵌入二进制（`//go:embed static/*`），再在真实浏览器里逐项操作。全部通过：

| 操作 | 结果 |
|---|---|
| 管理员登录 | ✅ |
| 用户：创建（`新建用户` 表单提交 `username` + `password`，落库 `role=user`） | ✅ |
| 用户：编辑（表头变「编辑用户」、用户名输入禁用、密码框留空并提示「留空保持原密码」） | ✅ |
| 用户：改密（经 UI 保存后 `users.password_hash` 前后不同，确认走的是 `PATCH`） | ✅ |
| 用户：行操作只有「编辑/停用」，无删除按钮（与能力表一致） | ✅ |
| API 密钥：创建后明文密钥仅显示一次，列表只显示掩码 | ✅ |
| 价格：列表无「操作」列，创建表单显示不可变提示；`/api/admin/prices/{id}` 的 `PATCH`/`DELETE` 返回 405 | ✅ |
| 预算 / 限流：行操作为「编辑/停用/删除」 | ✅ |
| 别名：重命名成功；重名返回 409 `alias_conflict`；禁用；删除返回 204 | ✅ |
| 路由：移除前弹出确认「确定移除路由「…」？」；取消后路由保留，确认后路由被删除 | ✅ |
| 供应商：创建响应不回显 `api_key`（凭证保持掩码） | ✅ |
| 错误信封：未带 CSRF 的写请求返回 `{error:{message,type,code,request_id}}` | ✅ |

### Phase 5 覆盖加固记录（G10/G11/G14 验收）

- **G10**：`internal/policy/policy_concurrency_test.go`（8 个用例，`sync.WaitGroup` + 原子计数）在 `-race` 下断言限流 5 时 200 并发只放行 5、并发上限 3 时 50 并发峰值不超过 3、release 回调幂等、并发预留金额无丢失更新、同一 settlement id 并发去重、预算到限即拒绝且低一分仍放行。覆盖率 68.2% → 80.4%。
- **G11**：新增 `internal/policy/counters.go`，把进程内策略状态（滚动请求/Token 窗口、并发计数、未落库预留金额、结算失败标记）收敛到导出的 `CounterStore` 接口，默认实现 `NewMemoryCounters()`，唯一替换点 `NewServiceWithCounters(store, counters)`。`policy.go` 的 `Acquire`/`Release`/`ObserveUsage`/`RecordCharge`/`RecordPendingSettlement`/`RecordSettlementFailure`/预算校验全部委托给该接口，锁序、解锁覆盖与「全有或全无」预留语义不变（行为等价重构）。**按方案非目标，未实现分布式版本**，V1 单实例边界保持显式声明。
- **G14**：新增测试抬升三个洼地，覆盖率见下表；数字已于 `92b2970` 用 `-coverpkg=./...` 合并 profile 复测刷新（`internal/store` 为 54.6%，其 SQL 聚合路径需要 `TEST_DATABASE_URL`）。

| 包 | 修复前 | 修复后（`7c9a377`） | 复测（`92b2970`） |
|---|---|---|---|
| `cmd/server` | 14.4% | 67.0% | 67.0% |
| `internal/model` | 40.5% | 100.0% | 100.0% |
| `internal/httpapi` | 46.6%（本机复测基线 50.9%） | 69.5% | 70.3% |
| `internal/policy` | 68.2% | 80.4% | 80.4% |
| `internal/store` | 52.6% | 54.0% | 54.6% |
| 全项目（`-coverpkg=./...` 合并 profile） | 约 54% | 67.4% | 67.7% |

Phase 5 的对抗性审查共提出 6 项发现，全部已处置。其中 4 项断言原本**无法失败**：`runMaintenance` 的取消分支因测试传 `nil` store 而成为死代码、`RouteStats` 零值往返属同义反复、价格版本列表只校验 `[` 前缀（`[]` 也能通过）、以及一处用被测值构造期望值的自比较断言。已逐一改为可证伪断言（传非空 store 使 `select` 真正可达、非零字段往返、断言 `"version":1` 与 `"gpt-4o"` 内容），并用「回退生产代码 → 观察断言以预期消息失败 → 还原并重新 `gofmt`」的方式逐条验证，避免新测试再次沦为同义反复。


## 9. 本次核对中的环境变更

- 删除 12 个无文件的残留空目录：`backend/cmd/gateway`、`backend/internal/{analytics,apikey,audit,auth,budget,logging,metering,provider,ratelimit,routing,web}`。这些目录不含任何源码，删除后 `go build ./...` 仍通过。`docs/PROJECT_ANALYSIS.md:56` 描述的目录结构中提到的 `internal/metering` 等模块实际未落地，相关能力分布在 `billing`、`policy`、`gateway` 包内。

## 10. 核对所用的验证命令

```bash
# 单一门禁（CI 与本地同一入口）
scripts/test.sh --strict                      # gofmt → vet → build → go test -race → 前端 build+test
make test TEST_DATABASE_URL='postgres://…'    # 同上，Makefile 封装
make webui                                    # 构建 Vue 控制台并嵌入 static/（再 make test 验证嵌入态）

# 覆盖率（合并 profile 后去重统计）
cd backend && TEST_DATABASE_URL='postgres://xd@localhost:5432/postgres?sslmode=disable' \
  go test -count=1 -coverpkg=./... -coverprofile=/tmp/cov.out ./... && \
  go tool cover -func=/tmp/cov.out | tail -1
```

运行时冒烟（本地 PostgreSQL 14.24，监听 `127.0.0.1:18090`）结果：`/healthz` 200、`/readyz` 200、`/` 200 `text/html`、无 key 访问 `/v1/models` 401、`POST /api/auth/login` 200（返回 session cookie、`csrf_token` 与 admin 用户）。启动日志显示 `schema_migrations` 迁移执行与首个管理员引导成功。

## 11. `92b2970` 增量核对（门禁幂等、CI 双 PG 腿、镜像实跑）

本轮在 `610acd0` 核对的基线上补做三项实跑验证，并修复由此暴露的两处缺陷。

### 11.1 门禁幂等性（已修复缺陷）

- 缺陷：`make webui` 会把真实 Vite 产物拷入 `backend/internal/httpapi/static/`，此后 `go test ./internal/httpapi/` 中原有的占位页断言会因包级 `frontendAssets` 已被真实控制台覆盖而**失败**——即「先构建前端再跑门禁」会红。`scripts/test.sh` 因此并非与构建状态无关的幂等门禁。
- 修复（`httpapi/ui.go`）：新增 `frontendFS = mustSub(frontendAssets, "static")`、`serveFrontend(fsys fs.FS, …)`（把 asset 文件系统改为参数）、`writeAsset`、`mimeType`。`ui_test.go` 重写为 10 个用例，改用 `testing/fstest.MapFS` 显式构造「未构建前端」与「已嵌入真实控制台」两种文件系统，不再依赖测试时 `static/` 的实际内容。
- 复现验证：`make webui && make test`（先嵌入真实控制台）与 `git clean` 后 `make test`（仅 `.keep`）**两种顺序均 13 包 ok、`All checks passed.`**。另新增 `TestRouterRejectsUnknownRoute` 断言 `/dashboard` 经真实路由返回 404（`handler.go:34-40` 的 `index()` 只把 `/` 与 `/assets/*` 交给前端 handler），并把原先误名为「SPA 回退」的用例正名为 `TestFrontendNormalisesExtensionlessPath`。

### 11.2 CI 双 PostgreSQL 腿本地实跑

用本地 Podman（`podman-machine-default`，libkrun，5.8.7 linux/arm64 rootless）拉取与 CI 完全一致的镜像 `postgres:16`（16.15 Debian aarch64）与 `postgres:14`（14.24 Debian aarch64），各以 CI 凭据（`gateway`/`gateway`/`gateway_test`）在宿主端口 55432 / 55433 启动，对每条腿跑完整 `make test TEST_DATABASE_URL=…`：**两条腿均 `All checks passed.`、13 包 ok、0 SKIP**，且 PostgreSQL 支撑的用例确实执行（`TestMigrationsAreIndividuallyIdempotent`、`TestPostgresAccountingIdempotencyTimestampsAndAnalytics`、`TestPostgresProviderHealthOrderAndSecretRotation`、`TestGroupsPostgresCRUDAndAccess`、`TestAliasRenameAndDeletePostgres`、`TestRunServesRequestsAndShutsDownGracefully`）。

### 11.3 镜像构建与端到端实跑（闭环 G13）

`podman build --format docker`（必须用 `docker` 格式，OCI 格式会静默丢弃 `HEALTHCHECK`）构建多阶段镜像 `llm-gateway:local`，与 `postgres:16-alpine` 同网络 `-p 18080:8080` 起容器后验证：`/healthz` 200、`/readyz` 200、`/` 200 `text/html; charset=utf-8` 且返回**真实 Vue 控制台**（标题 `LLM Gateway 管理控制台`、引用 `/assets/index-BQG5W-qe.js`，全文 0 处 `This is a placeholder page`）、该资源 200 `text/javascript` 116490 字节、缺失资源 404、无 key `/v1/models` 401、`POST /api/auth/login` 200（`csrf_token` + admin 引导）、`/api/auth/me` 200、`/api/admin/runtime` 200。

### 11.4 由镜像实跑暴露的迁移加载器缺陷（已修复）

- 缺陷：宿主 macOS 打包上下文时产生的 AppleDouble 边车文件 `._0001_init.sql` 会被 `//go:embed migrations/*.sql` 通配符匹配（Go 的 `.`/`_` 排除仅作用于目录展开，不作用于显式通配符）。该文件是二进制，应用时以 `pq: invalid message format (08P01)` 中断启动，且因 `.` 排在 `0` 之前会**先于**真实迁移执行。
- 修复（`store/store.go`）：抽出 `migrationNames(fsys fs.FS) ([]string, error)`，跳过目录、`._` 前缀与非 `.sql` 文件并排序；`Migrate` 改用它。新增 `TestMigrationNamesFiltersEmbeddedEntries`（`fstest.MapFS` 断言仅保留 `0001_a.sql`/`0002_b.sql`）与 `TestEmbeddedMigrationsAreSortedSQL`（对真实 `migrationsFS` 断言有序且仅 SQL）。

### 11.5 其他初始化增量

- 根 `Makefile`（`fmt`/`vet`/`webui`/`build`/`test`/`test-backend`/`test-frontend`/`run`/`clean`，导出 `TEST_DATABASE_URL`）、根 `LICENSE`（专有占位，待替换法律实体）、`.gitignore` 增补 `.pi/` 与 `static/*`（保留 `.keep`）。
- 前端 Vitest 基建：`vitest` + `jsdom` + `@vue/test-utils`，`npm run test`；新增 `frontend/src/api.spec.ts`（15）与 `DashboardPane.spec.ts`（2），共 17 用例全部通过。
- 顺带修复 `frontend/src/api.ts` 的网络错误路径：原 `catch` 会泄漏英文 `Failed to fetch`，改为返回本地化 `网络错误`，并补测试。

## 12. 登录放行与普通用户自助能力（`56a78a8`、`a0bdf4e`＝只读视图；`a666f00`＝自助读写）

现场反馈「新增的用户无法登录，显示用户名密码错误」。核对后确认这不是密码问题：登录接口只放行 `role=admin`，而控制台「新建用户」的默认角色是「用户」，两者叠加使任何新建账号即使密码正确也得到 `401 invalid_credentials`，前端据此显示「用户名或密码错误」。按方案 A（小型 RBAC 改造，而非只改提示文案）处置如下。

### 12.1 后端行为变更

- **任何启用账号都能登录**：`auth.go:155` 的 `login` 先验密码，只有密码正确但账号被停用时才返回 `403 account_disabled`（`auth.go:207`）；密码错误与账号不存在统一为 `401 invalid_credentials`。路由注册 `auth.go:108-110`。
- **账号枚举时序加固**：`auth.go:28-48` 以 `sync.OnceValue` 懒计算一个格式合法的 Argon2id 假哈希（而非硬编码字符串——参数一旦变化，硬编码会因格式不符走 `VerifyPassword` 的快速失败路径，反而恢复时序差），用户名不存在的 `ErrNotFound` 分支与 `user == nil` 分支各走一次同代价校验（`auth.go:182,190`）。
- **新增自助端点 `/api/self`**：挂载于 `auth.go:115-116`，实现见 `self.go`；取数一律以会话账号为准，请求体与查询串里的 `user_id` 被忽略（`self.go:443-450` 注释与实现一致）。
- **写操作校验 CSRF**：`self.go:83` 鉴权通过后对非 `GET/HEAD` 校验会话 CSRF，失败 `403 csrf_failed`。
- **管理面仍仅管理员**：`admin.go:55`、`groups.go:59` 对有效非管理员会话返回 `403 administrator_required`（无会话仍 `401`），角色检查先于 CSRF。
- **改密撤销其它会话**：`self.go:415-425`（调用在 `self.go:420`）调用 `store/users.go:122` 的 `DeleteUserSessionsExcept`，保留发起改密的那个会话；撤销失败返回 `500 session_revocation_failed`。
- **审计**：新建/更新/删除密钥与改密分别写 `self API key created` / `updated` / `deleted` / `self password changed`（`self.go:295,335,349,426`），明细不含密码。

### 12.2 `/api/self` 验收项（新增）

| 序号 | 验收项 | 判定 | 证据 | 缺口 |
|---|---|---|---|---|
| 1 | 任何启用账号可登录；密码错误与账号不存在同为 401 且不建会话 | ✅ | `auth.go:155,182,190`；`auth_test.go:208,371` | — |
| 2 | 停用账号密码正确时 403 `account_disabled`，且既有会话立即失效 | ✅ | `auth.go:207`；`auth_test.go:135,333` | — |
| 3 | 普通用户只能读到自己的数据（`user_id` 等过滤参数被忽略） | ✅ | `self.go:224`（`apiKeys` 按会话账号取数）、`self.go:443-450`（查询串过滤参数被忽略）；`self_test.go:325,359,382` | — |
| 4 | 自助写操作必须带 CSRF，缺失即 403 | ✅ | `self.go:83`；`self_test.go:495` | — |
| 5 | 可新建自己的密钥：明文只回一次，落库为哈希 | ✅ | `self.go:266,295`；`self_test.go:525,569`；前端 `App.vue:357,375,383`（表单与本地校验）、`App.vue:798-808`（创建弹窗）、`api.ts:66-70`；`MemberConsole.spec.ts:294,527` | — |
| 6 | 可停用/启用、删除自己的密钥 | ✅ | `self.go:301,341,349`；`self_test.go:581,739`；前端 `App.vue:457,479`（写操作）、`App.vue:748,750-751`（列表与按钮）、`api.ts:71-72` | — |
| 7 | 越权（他人或不存在）的密钥 id 一律 404，不泄漏存在性 | ✅ | `self.go:356`；`self_test.go:626` | — |
| 8 | 可修改自己的密码：校验当前密码、≥8 字符、不得与旧密码相同 | ✅ | `self.go:378-428`（三处校验在 `self.go:394-405`）、`minSelfPasswordLength` `self.go:25`；`self_test.go:660,693,711` | 管理端改密仍无长度校验，见 §12.3 |
| 9 | 改密后注销本账号其它会话，发起改密的会话保留 | ✅ | `self.go:415-425`（调用在 `self.go:420`）；`store/users.go:119-122`；`store/sessions_test.go:24` | — |
| 10 | 自助写操作写入审计且不含密码 | ✅ | `self.go:295,335,349,426`；`self_test.go:295,569,643,693` | — |
| 11 | `/api/admin/**` 对普通用户仍 403（无会话 401） | ✅ | `admin.go:55`；`groups.go:59`；`auth_test.go:306` | — |
| 12 | 受限视图不发起任何 `/api/admin/**` 或 `/readyz` 请求 | ✅ | `MemberConsole.spec.ts:146,175,294` 中每个用例都断言 `managementCalls()` 为空；`App.vue:246` 把非管理员导航全部交给 `App.vue:261-290` 的 `loadMemberView`，它只走 `/api/self/**` | — |
| 13 | 错误文案：登录放行与自助写共 12 条映射，另有 6 条自助路径会遇到的存储/分页文案，且与后端 message 逐字对应 | ✅ | `errors.ts:34-35,41-50,53-58`；`errors.spec.ts:40,55,65`；`MemberConsole.spec.ts:146,497` | — |
| 14 | 受限视图新建密钥可填过期时间；明文密钥以弹窗给出（关闭即消失）；密钥列表含「最后使用」列 | ✅ | 后端契约 `self.go:217,276,518`（`expires_at` 可空、必须是未来时刻）；前端 `App.vue:375-381`（本地换算 UTC 并拦截过去时刻）、`App.vue:798-808`（创建弹窗）、`App.vue:810-818`（明文弹窗，含复制）、`App.vue:347,750`（最后使用列）、`styles.css:98,155-161,206-209`；`MemberConsole.spec.ts:294,527,564,584,611`、`api.spec.ts:210` | — |

### 12.3 已知边界（本次未做，显式记录）

- **管理端改密不撤销会话、也无密码强度校验**：`/api/admin/users/{id}` 的改密只有解码与落库，强度下限 8 字符与「撤销其它会话」只作用于自助路径（`self.go:25`、`self.go:415-425`）。管理端改密是管理员本人发起的操作，暂按现状保留；如与验收条款冲突应单独立项。
- **自助改密只支持「当前密码换新密码」**，不提供找回密码或邮箱验证。
- **登录锁定仍只按来源限流**（`login_limit.go:26` 的按来源计数、`login_limit.go:78` 的 429 `login_rate_limited`），没有账号维度的失败计数锁定。
- **受限视图仍不提供密钥白名单（`model_allow`）的编辑 UI，也不能在创建后修改过期时间**：后端 `PATCH /api/self/api-keys/{id}` 已支持这两个字段（`self.go:301-335`），控制台目前只在「新建密钥」弹窗里录入过期时间（`App.vue:375-381`、`api.ts:66-70`），名单编辑与创建后改期都不在界面上。

### 12.4 本次核对的边界（验证欠账）

- **§12.1/§12.2 的行号已按当前工作树逐条复测**：改动落地后重新用 `grep -n` 实测了每一条引用并改到实测值（如 `self.go:378-403` → `378-428`、`self.go:418-425` → `415-425`（调用在 `420`）、`errors.ts:34-50` → `34-35,41-50,53-58`）。第三次改造（密钥弹窗与「最后使用」列）落地后又复测过一遍前端引用：`App.vue:357,375,383,410,449`（逻辑，按函数名检索）、`App.vue:748-755,798-808,810-818`（模板）、`styles.css:98,155-161,206-209`、`MemberConsole.spec.ts:146,175,294,497,527,564,584,611`、`api.spec.ts:210`；提交 `2efe9f1` 加了剪贴板兜底后 `copyMemberSecret` 变长，`App.vue` 的模板行号随之下移 25 行（脚本区新行号为 `357,375,383,410,424,449`，模板区为 `748-755,798-808,810-818`），引用这些位置时请以函数名或元素文本检索。
- **本地端到端**：`$PI_SCRATCH_DIR/verify-self.sh` 在真实进程（本地 PostgreSQL + `:18099`）上跑 9 组、42 项断言全部通过——含「无 CSRF 403」「新建密钥 201 且明文只回一次」「明文密钥在数据面可用（`/v1/chat/completions` 非 401）」「停用后立刻 401、重新启用后恢复」「他人 PATCH/DELETE 我的密钥 404 `not_found`」「伪造 `user_id` 被忽略（归属仍为会话账号）」「改密四态（403/400/400/204）」「旧密码登录 401、改密前的另一会话被撤销 401、发起改密的会话仍 200」「审计无密码」「普通用户会话从未请求 `/api/admin/**`」。
- **部署后线上复核**：`$PI_SCRATCH_DIR/online-verify.sh` 对 `http://172.20.80.130:8080`（容器内运行的是本提交树的二进制）跑 8 组、73 项断言全部通过：新建普通账号 → 登录 200 → 受限视图；自助密钥的创建/停用/启用/删除与数据面「密钥可用 / 停用即 401」联动；越权（管理员会话、另一个普通账号）一律 404 `not_found`；改密四态、其它会话被撤销而当前会话保留；审计记录三项且不含密码；后端容器日志不含密码与密钥明文；线上 bundle 含新文案。同一账号随后在真实浏览器中复核：登录后侧栏只有「概览／我的 API 密钥／我的调用记录」，浏览器 `performance` 资源记录里全部是 `/api/auth/*` 与 `/api/self/*`（无 `/api/admin/**`、无 `/readyz`）；密钥的停用/删除/改密提示与列表刷新都符合预期。
- **第三次改造（密钥弹窗、过期时间、「最后使用」列）的复核**：本地 `npx vue-tsc --noEmit` 干净、`npm test` 63/63（`MemberConsole.spec.ts` 21 例）、`make test`（`-race` + 前端构建与用例）全绿。`online-verify.sh` 扩到 8 组 **88 项断言全绿**，新增一组直查本轮依赖的接口契约：`expires_at` 原样回传、缺少 `expires_at` 时为 `null`（界面显示「永不过期」）、过去的时刻 400 `invalid_expiry`、新建的密钥 `last_used_at` 为 `null` 而数据面调用后变为时间戳（「最后使用」列的数据来源）、列表仍无明文与 `key_hash`；线上 bundle 另断言含「留空表示永不过期」「关闭后无法再次查看」「最后使用」「复制失败」且不再含「创建密钥」（旧的内联建表单按钮）。真实浏览器在**线上** `http://172.20.80.130:8080` 复核同一账号：受限侧栏 →「我的 API 密钥」→ 弹窗填名称与过期时间 → 提交体为 `{"name":"ui3-online","expires_at":"<UTC>"}` → 明文只在「密钥已创建」弹窗里出现；关闭弹窗后 `document.body.textContent` 中已无明文；用该明文在页面内调用 `/v1/chat/completions`（本机无上游模型，404）后刷新，列表行显示「过期 2026/10/22 19:57:00」与「最后使用 2026/9/22 19:58:01」。该主机是 **http://（非安全上下文）**，`navigator.clipboard` 不存在，复制按钮因此走 `document.execCommand('copy')` 兜底：带真实用户手势（CDP `Runtime.evaluate` + `userGesture:true`）时提示「已复制到剪贴板。」，无手势时序拒绝则会显示「复制失败，请手动选中密钥文本后自行保存」——两条路径都在线上实测过。
- **部署产物一致性（提交 `2efe9f1`）**：容器内 `/usr/local/bin/server` 的 SHA256 为 `726308be0d6b9b0e8e4f7d64b3a641bd392eb9c01b91f36f032d634fb7380e45`，与本地按该提交（复制整个 `backend/` + `frontend/dist` 后 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w'`）重建的二进制逐字节相同；线上 `assets/index-CWoDI-UX.js` 的 SHA256 为 `b65a0efecfb5b773f9f18e5b27f426aeefd87482690661892e6a93c94f690989`，与本地 `frontend/dist` 中同名文件一致（另一个本地重建目录只复制了 `cmd/` 与 `internal/`，产出 `090125d5…`；差异来自未复制的源码树内容，不是部署问题——全量复制的重建与容器完全一致）。镜像 ID `sha256:95380872b74cec8ffc73c48ce39af91c711f4dde1f7532602f3639a7c22cb6a1`。
- **线上探针数据清理**：本轮 8 个 `probe*` 账号与其 2 个密钥、5 条请求已删除（按 `username like 'probe%'` 精确限定，`api_keys`/`sessions`/`budget_rules` 由外键级联；`audit_log` 按不可篡改保留，现存 69 条 `self %` 记录）。清理后线上只剩 `admin:admin:true`、`test:user:true`、`console-check:user:false`，被删账号登录 401，两个容器 `healthy`。
- **§2–§8 中不涉及本次改动文件的引用仍未整体复测**：`92b2970` 之后工作树前进 140+ 个文件（新增 `scripts/e2e/`、限流目标名解析、供应商探测原因等），`gateway/`、`billing/`、`adapter/`、`policy/`、`store/queries.go`、`OperationsPane.vue` 等文件的行号很可能同样偏移。这些引用在按当前提交重新核对前不应作为定位依据；引用同一 PR 的代码请以符号名（函数名）检索，而不是行号。
