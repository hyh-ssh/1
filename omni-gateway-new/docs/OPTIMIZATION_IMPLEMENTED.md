# 优化实施记录

本轮按 `ai-gateway` 对比计划完成了可靠性、协议和运维增强。

## 已完成

- Provider 健康状态、cooldown、失败退避、`Retry-After`、验证时间、并发上限和成功/失败计数。
- 模型组/用户组授权，和 API Key 白名单取交集；`/v1/models` 按调用方过滤。
- 原生 `/v1/messages`、`/v1/embeddings`、`/v1/images/generations`，包括 SSE、usage、模型改写和凭证隔离。
- 路由顺序原子更新，拒绝跨别名或不完整的 route ID 列表。
- 有界持久化队列、重试、幂等 settlement ID、账本时间戳保留和运行时失败计数。
- 价格/账本统计增加两条账本（上游成本与积分消耗）、分位数、路由表现和 API 过滤。
- 主密钥轮换、内容日志轮换、Argon2id 参数校验、登录限速和审计事件。
- Provider URL SSRF 防护、禁用重定向、私网地址策略和请求/响应大小上限。
- 管理界面增加仪表盘趋势、健康状态、连接验证、模型目录、分组授权、路由排序和请求账本明细。
- 请求 ID 分为客户端追踪 ID 和网关生成的记录 ID，重复的客户端 ID 不会合并账本或内容日志；流式响应在首帧前失败时仍返回结构化错误，首帧后只发送安全的中断事件。
- 上游未返回 usage 时对纯文本输入/输出采用可审计的 `text_bytes_v1` 粗略估算，并在 `estimated=true` 与 `source` 字段中显式标记；图片、Base64 和任意元数据不计入文本估算。
- Store 已统一使用 XORM Engine 管理 PostgreSQL 连接池、上下文查询、事务和嵌入式迁移，业务层不再直接依赖底层驱动连接池。

## 验证

```bash
cd backend
go test -mod=readonly ./...
go test -race -mod=readonly ./...
go vet -mod=readonly ./...
TEST_DATABASE_URL='postgres://.../postgres?sslmode=disable' go test ./internal/store -count=1
cd ../frontend
npm run build
```

本轮完整包测试、竞态测试、`go vet`、PostgreSQL 集成测试、前端生产构建和
Docker Compose 配置校验均已通过。全项目 Go 语句覆盖率当前约 54%；新增的
background、billing、config、crypto、protocol、upstream 模块达到或超过 80%，
既有大型 HTTP/store 路径仍低于目标，后续应按接口继续补测试。

多实例预算/限流仍需共享协调存储；当前有界队列在进程崩溃时仍可能丢失尚未完成的内存任务，运行时指标会暴露失败和拒绝数量。

部署时先设置新的 `GATEWAY_MASTER_KEY`，并将旧密钥放入
`GATEWAY_PREVIOUS_MASTER_KEYS`，让实例启动完成轮换后再移除旧密钥。轮换在
同一事务中处理 provider 凭证和加密内容日志，任何一条旧密钥无法解密都会回滚。

## 缺口修复轮次（Phase 0-5）

`docs/ACCEPTANCE_GAPS.md` 的 G1-G14 按「P0 结算完整性 → P1 契约健壮性 → P2 前端与测试加固」分阶段修复，
每个 Phase 一个提交并同步更新活账。

### Phase 5 覆盖加固（G10/G11/G14）

- **G10 并发测试**：`internal/policy` 新增 `policy_concurrency_test.go`（8 个用例），在 `-race` 下断言
  RPM 计数精确（限流 5、200 并发只放行 5）、并发上限不超发（限流 3、50 并发峰值不超过 3）、
  release 回调幂等、并发预留金额无丢失更新、同一 settlement id 并发去重、预算到限即拒绝且低一分仍放行。
- **G11 接口接缝**：新增 `internal/policy/counters.go`，把进程内策略状态（滚动请求/Token 窗口、并发计数、
  未落库预留金额、结算失败标记）收敛到导出的 `CounterStore` 接口，默认实现 `NewMemoryCounters()`；
  `NewServiceWithCounters(store, counters)` 是唯一替换点。V1 仍是单实例，未实现分布式版本。
- **G14 覆盖率**：新增测试把三个洼地拉起来。

| 包 | 修复前 | 修复后 |
|---|---|---|
| `cmd/server` | 14.4% | 67.0% |
| `internal/model` | 40.5% | 100.0% |
| `internal/httpapi` | 50.9% | 69.5% |
| `internal/policy` | 68.2% | 80.4% |
| 全项目（`-coverpkg=./...`） | 约 54% | 67.4% |

验证命令与结果：

```bash
cd backend
gofmt -l .                                    # 无输出
go vet -mod=readonly ./...                    # 通过
go build ./...                                # 通过
TEST_DATABASE_URL='postgres://xd@localhost:5432/postgres?sslmode=disable' \
  go test -mod=readonly -race -count=1 ./...  # 13 个包全部 ok
cd ../frontend && npm run build               # vue-tsc --noEmit && vite build 通过
```

`internal/store` 仍为 54.0%：其 SQL 聚合路径需要 `TEST_DATABASE_URL` 才计入覆盖率，未设置该变量的
环境会给出显式警告而不是静默跳过。

> 注（`92b2970` 复测）：上表数字为 Phase 5 当时的记录。以 `-coverpkg=./...` 合并 profile 去重后复测，
> `internal/httpapi` 已是 **70.3%**、`internal/store` **54.6%**、全项目 **67.7%**；其余包不变。
> 详见 `docs/ACCEPTANCE_GAPS.md` §8（G14）与 §11。
