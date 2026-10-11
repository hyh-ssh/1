# omni-gateway 项目分析与实施状态

> 文档日期：2026-08-29  
> 状态：整体程序设计与可运行 Go + PostgreSQL + Vue 单体基线已完成
> 依据：此前需求分析、当前仓库代码及本次编译检查

## 1. 项目目标

本项目拟实现一个受 LiteLLM 启发、但范围更聚焦的 LLM 网关，主要面向单组织内部使用。

核心目标如下：

1. 提供统一的 OpenAI 风格调用入口，屏蔽不同上游模型供应商的接口差异。
2. 通过简化的供应商配置完成模型接入、模型别名和故障转移路由。
3. 记录请求、上游尝试、用量、延迟、错误和费用，形成详细统计。
4. 支持上游成本价（货币 CNY）与积分价（积分）两条计费账本、不可变价格版本和滚动预算控制。
5. 支持用户、网关 API Key、模型权限、限流和后台管理。
6. 使用 Vue 3 前端与 Go 后端，并最终以单体方式部署。

## 2. 总体架构方向

建议的最终形态为一个 Go 单体服务：

```text
                       ┌──────────────────────────┐
                       │       Vue 3 管理前端      │
                       │  仪表盘 / 配置 / 日志等   │
                       └─────────────┬────────────┘
                                     │ 管理 API
                                     ▼
┌──────────────┐       ┌──────────────────────────┐       ┌───────────────────┐
│ SDK / 应用方  │──────▶│       Go Gateway         │──────▶│ 上游 LLM Provider │
│ OpenAI API   │       │                          │       │ OpenAI-compatible │
└──────────────┘       │ 认证、路由、适配、预算、   │       │ Anthropic/Gemini │
                       │ 限流、计费、日志、统计      │       └───────────────────┘
                       └─────────────┬────────────┘
                                     │
                                     ▼
                       ┌──────────────────────────┐
                       │        PostgreSQL        │
                       │ 配置 / 请求 / 账本 / 聚合 │
                       └──────────────────────────┘
```

### 2.1 单体部署含义

计划由同一个 Go 进程承担：
- 网关兼容 API；
- 管理 API；
- Vue 3 编译产物的静态文件服务；
- 数据库迁移；
- 后台聚合、过期会话清理等轻量任务；
- 优雅关闭。
当前已建立服务入口、HTTP 路由、认证、三类 Chat Adapter、网关编排、计费策略、统计查询、Vue 管理端和 Docker 构建；后续增强集中在完整前端编辑体验、分位数图表、原生 Provider Responses 状态协议和分布式部署。

### 2.2 当前目录结构

```text
llm/
├── backend/
│   ├── cmd/server/          # Go 启动入口和维护任务
│   └── internal/
│       ├── adapter/        # 上游协议适配器
│       ├── billing/        # 价格和账本结算
│       ├── config/         # 环境变量配置
│       ├── crypto/         # 密钥加密、密码和 API Key 处理
│       ├── gateway/        # 别名路由、策略、记录和网关编排
│       ├── httpapi/        # 公共 API、管理 API 和嵌入静态资源
│       ├── model/          # 领域实体、记录、用量和统计
│       ├── policy/         # 预算和限流策略
│       ├── protocol/       # 统一协议模型
│       └── store/          # XORM PostgreSQL 数据访问与迁移
├── frontend/               # Vue 3 + TypeScript + Vite 管理端
├── Dockerfile              # Go/Vue 多阶段单体构建
├── docker-compose.yml      # PostgreSQL、后端和可选前端预览
└── docs/
    └── PROJECT_ANALYSIS.md
```

## 3. 核心领域设计

### 3.1 用户与访问凭证

当前模型将系统视为单组织、多用户环境。

- 用户角色：`admin`、`user`；
- 后台登录：服务端 Session + CSRF Token；
- 调用网关：独立 API Key；
- API Key 仅保存 SHA-256 哈希、前缀和尾部四位；
- API Key 可设置有效期、启停状态和模型白名单；
- 管理员账号计划在首次启动时引导或自动初始化。

相关代码：

- `backend/internal/model/entities.go`
- `backend/internal/store/users.go`
- `backend/internal/store/apikeys.go`
- `backend/internal/crypto/crypto.go`

### 3.2 供应商配置

供应商按“接口协议类型”而不是“厂商品牌”分类，这是简化配置的重要原则。

当前定义的类型：

- `openai_compatible`
- `anthropic`
- `gemini`

一个供应商的最小配置包括：

| 字段 | 含义 |
|---|---|
| `name` | 管理端显示名称 |
| `type` | 接口协议类型 |
| `base_url` | 上游基础地址 |
| `api_key_enc` | 加密保存的上游凭证 |
| `extra_headers` | 自定义请求头 |
| `timeout_ms` | 上游超时 |
| `active` | 是否启用 |

供应商凭证使用 AES-256-GCM 加密，主密钥通过环境变量注入，不写入数据库。

简化配置的目标交互应为：

1. 选择接口类型；
2. 输入名称、Base URL 和 API Key；
3. 可选设置自定义 Header 和超时；
4. 测试连接或拉取模型列表；
5. 选择模型并配置能力；
6. 将模型挂载到对外模型别名。

已实现 OpenAI-compatible、Anthropic Messages 和 Gemini generateContent 三种 Provider Adapter；Responses 入口仍只对 OpenAI-compatible Provider 可用，原生 Anthropic/Gemini 的 Responses 状态协议不提供。

### 3.3 模型、别名与路由

模型被拆分为三个概念：

1. **ProviderModel**：供应商实际提供的上游模型；
2. **ModelAlias**：调用方看到的统一模型名；
3. **RouteEntry**：别名下按优先级排列的上游目标。

示例：

```text
对外别名：general-chat
  priority 10 -> Provider A / gpt-4o-mini
  priority 20 -> Provider B / compatible-model-x
```

调用方只使用 `general-chat`。网关根据路由项依次尝试上游，当发生可重试错误时执行故障转移。

当前错误分类中，下列情况可触发故障转移：

- 连接失败；
- 超时；
- HTTP 429；
- HTTP 5xx。

认证错误和普通 4xx 默认不可重试，避免把无效请求扩散到所有供应商。

模型能力目前包括：

- 流式输出；
- 多模态；
- Tool Calling；
- Structured Output；
- Reasoning；
- 原生有状态 Responses API。

### 3.4 统一协议层

`backend/internal/protocol/types.go` 定义了供应商无关的数据结构，当前覆盖：

- Chat Completions 请求与响应；
- Responses 请求与响应；
- 文本和图像内容块；
- Tool 定义和 Tool Call；
- 流式事件；
- 可扩展的透传字段。

适配器接口位于 `backend/internal/adapter/adapter.go`，提供：

- `Chat`
- `ChatStream`
- `Responses`
- `ResponsesStream`
- `ListModels`

这一层的作用是将调用入口与具体供应商协议解耦。

### 3.5 用量模型

用量不是固定的“输入 Token + 输出 Token”结构，而是可扩展 Meter Map：

```json
{
  "meters": {
    "input": 1200,
    "output": 300,
    "cache_input": 500,
    "reasoning": 80
  },
  "estimated": false
}
```

可扩展 Meter 可以继续表达：

- `cache_creation`
- `image`
- `audio`
- `requests`
- 其他供应商特有计量单位

这种设计有利于兼容不同供应商的定价结构，并让账本保持可扩展。

### 3.6 价格、计费与账本

系统区分：

- **Cost**：上游真实成本，货币（CNY，¥）；
- **Credits**：向内部用户或业务方计收的积分消耗，非货币且可为小数，保留三位小数。

所有金额使用 `shopspring/decimal`，禁止使用浮点数结算；上游成本默认币种语义为人民币 CNY，积分金额保留三位小数。

价格采用不可变版本：

```text
PriceVersion
├── model_id
├── version
├── cost[meter]     # 上游成本，货币 CNY（¥）
├── credits[meter]  # 积分价，积分（3 位小数）
└── created_at
```

每次结算应将使用的价格版本写入账本，确保未来调整价格后，历史费用仍可审计和重算。

账本按 Meter 拆行记录：

```text
request_id + user + key + model + provider + meter
+ quantity + cost + credits + price_version + estimated
```

建议的结算规则：

- 上游每次实际尝试产生的成本都应计入 Cost；
- 只有最终成功请求才产生 Credits（积分）；
- 若上游未报告 Usage，可使用本地估算并标记 `estimated=true`；
- 请求记录保存汇总值，账本保存不可变明细。

> 交叉引用：积分标定约定（house 默认 0.0002 / 0.0004 积分每 token）、两本账不可相加（credits 是非货币单位，与 CNY 之间没有存储汇率），
> 以及正向迁移策略（`schema_migrations` 只记录版本名，`0005_credits.sql` 用存在性守卫执行 `RENAME COLUMN`，无反向迁移）
> 均已写入 README 的「积分 (credits) 约定」小节。

### 3.7 预算控制

当前预算规则按用户设置滚动时间窗口，分别支持：

- 上游成本上限（货币 CNY）；
- 积分上限（积分）；
- 启停状态。

例如：

```text
用户 A：过去 86,400 秒内积分不得超过 100 积分
用户 B：过去 2,592,000 秒内 Cost 不得超过 1,000 CNY
```

数据库访问层实现窗口内 Cost/Credits 汇总查询；网关在请求前检查已用预算，并在单实例内对启用预算的同一用户串行化，同时用内存中的已结算金额覆盖异步账本写入窗口。达到上游成本或积分上限时拒绝请求。

对于多实例部署，需要将预算锁和实时累计迁移到共享数据库预留表或分布式协调组件。

### 3.8 限流

当前限流模型支持：

- Scope：用户、API Key、模型；
- RPM：每分钟请求数；
- TPM：每分钟 Token 数；
- Concurrency：并发数；
- 可选模型维度。

当前策略服务已执行用户、API Key 和模型维度的 RPM、TPM、Concurrency 限制；计数器是单进程滚动窗口，预算用户会在一次请求生命周期内持有串行化锁。多实例部署必须替换为共享限流/锁实现。

### 3.9 请求、尝试与日志

日志分为三个层级：

#### Request

表示调用方发起的一次网关请求，记录：

- 用户和 API Key；
- API 类型和流式标记；
- 对外模型别名；
- 最终状态和 HTTP 状态码；
- 错误分类与摘要；
- 聚合 Usage；
- 总 Cost 和 Credits；
- 总延迟、上游延迟、TTFT；
- 输出 Token/s；
- Client IP、User-Agent 和标签。

#### Attempt

表示一次具体上游调用，用于分析故障转移：

- 供应商与上游模型；
- 路由优先级；
- 成功或失败；
- 状态码和错误分类；
- 本次 Usage 与 Cost；
- 延迟和 TTFT。

#### Content Log

用于可选保存请求和响应正文：

- 默认全局关闭；
- 支持单条内容大小限制；
- 表结构允许保存加密后的二进制正文；
- 可记录内容是否被截断。

内容日志涉及敏感数据，建议管理端明确提示风险，并限制只有管理员能够查看和解密。

### 3.10 审计日志

后台配置变更应写入审计日志，字段包括：

- 操作者；
- 动作；
- 资源类型和资源 ID；
- 变更摘要；
- 来源 IP；
- 时间。

当前已具备插入和查询审计日志的数据访问方法，并由管理 API 变更处理器记录管理员变更。

## 4. 关键请求流程

建议的完整 Chat/Responses 调用链如下：

```text
1. 接收请求并生成/透传 x-request-id
2. 校验网关 API Key
3. 校验用户、Key 状态、过期时间和模型白名单
4. 规范化请求协议
5. 解析模型别名与有序路由
6. 检查模型能力是否满足请求
7. 执行 RPM / TPM / Concurrency 限流
8. 检查预算并进行必要预留
9. 依次调用可用上游
   ├── 成功：返回结果
   ├── 可重试错误：记录 Attempt 后尝试下一路由
   └── 不可重试错误：立即失败
10. 汇总 Usage、Cost、Credits 和性能指标
11. 写入 Request、Attempt 和 Usage Ledger
12. 可选写入加密 Content Log
13. 流式请求发送标准 SSE 结束事件
14. 异步或同步更新统计聚合
```

### 4.1 流式调用注意事项

流式请求不能只在收到完整响应后处理。需要实时记录：

- 首个有效内容块时间，用于计算 TTFT；
- 输出 Token 数或上游最终 Usage；
- `finish_reason`；
- Tool Call 的增量参数；
- 客户端断开；
- 上游中断和解析错误；
- 最终 Usage 与结束事件。

如果客户端已经收到部分内容，再发生故障通常不能安全切换到另一个上游，否则会造成重复或语义不连续。建议只允许在“尚未向客户端发送任何有效内容”前进行流式故障转移。

## 5. 详细统计设计

### 5.1 建议展示指标

管理仪表盘至少应包含：

- 请求数、成功数、失败数、成功率；
- 输入、输出、总 Token；
- 上游成本（¥）与积分消耗（积分）；
- 平均延迟、P50、P95、P99；
- 平均 TTFT、P95 TTFT；
- 平均输出 Token/s；
- 429、5xx、超时、连接错误数量；
- 故障转移次数和各路由成功率；
- 估算 Usage 占比；
- 预算使用率；
- 活跃用户和活跃 API Key。

### 5.2 建议统计维度

- 时间；
- 用户；
- API Key；
- 对外模型别名；
- 供应商；
- 上游模型；
- API 类型；
- 是否流式；
- 最终状态；
- 错误类型。

数据库迁移已经预留小时/天聚合表，但当前聚合表仅保存平均值计算所需的 Sum 字段，不能直接支持精确 P50/P95/P99。分位数统计可采用以下方案之一：

1. 在查询窗口内直接从 `requests` 表计算；
2. PostgreSQL `percentile_cont` 定时生成独立分位数聚合；
3. 后续引入直方图或摘要结构。

V1 建议在受限时间范围内从明细表计算分位数，常用趋势图使用小时/天聚合表。

## 6. 安全设计

当前代码体现的安全方向：

- 上游 API Key 使用 AES-256-GCM 加密保存；
- 管理员和用户密码使用 Argon2id；
- 网关 API Key 仅保存 SHA-256 哈希；
- 管理端采用服务端 Session；
- 配置中预留 CSRF Token；
- 内容日志默认关闭；
- 所有数据库时间使用 UTC，业务边界使用配置时区。

仍需补充：

- 登录限速和失败锁定；
- 主密钥轮换方案；
- SSRF 防护或供应商 Base URL 管理策略；
- 分布式多实例下的限流与预算协调；
- 更完整的日志保留与脱敏策略。

## 7. 当前实现状态

### 7.1 已建立的基础

| 模块 | 状态 | 说明 |
|---|---|---|
| Go Module | 部分完成 | 已定义模块、XORM PostgreSQL Store、decimal 和 crypto 依赖 |
| 运行配置 | 基础完成 | 监听地址、数据库、主密钥、管理员、时区、内容日志 |
| 加密工具 | 基础完成 | AES-GCM、Argon2id、API Key 哈希 |
| 领域模型 | 基础完成 | 用户、Key、Provider、模型、路由、价格、预算、限流 |
| 请求记录模型 | 基础完成 | Request、Attempt、Ledger、Audit |
| 统一协议 | 基础完成 | Chat、Responses、工具、多模态、流式事件 |
| Adapter 接口 | 基础完成 | Chat、Responses、Streaming、Models |
| OpenAI-compatible Adapter | 阶段 1 完成 | 非流式、Chat/Responses SSE、Usage、工具、多模态和请求字段映射已补齐，并已增加单元测试 |
| PostgreSQL Store（XORM） | 基础 CRUD | 用户、Key、Provider、路由、价格、预算、限流、记录；连接池、事务和迁移统一由 XORM Engine 管理 |
| 数据库迁移 | 初版完成 | 已覆盖当前主要实体 |
| HTTP 服务 | 基础完成 | `cmd/server` 启动入口、配置/密钥/数据库初始化、迁移、健康检查、就绪检查、Request ID 和优雅关闭已实现 |
| 网关编排 | 阶段 3/4 基础完成 | Chat/Responses 路由、OpenAI-compatible/Anthropic/Gemini Chat Adapter、模型别名、顺序故障转移、流式代理、Request/Attempt、Cost/Credits 两条账本和策略检查已实现；原生 Provider 的 Responses 状态协议不提供 |
| 统计服务 | 基础完成 | 请求聚合查询、Stats API 和单体维护任务已实现；更丰富的图表与分位数仍待补充 |
| Vue 3 前端 | 基础完成 | 已建立登录、仪表盘和管理资源导航；复杂表单与图表仍待扩展 |
| 单体打包 | 基础完成 | Docker 多阶段构建会编译 Vue 并嵌入 Go 静态资源，同时提供独立前端镜像 |
| 自动化测试 | 基础完成 | Adapter、HTTP 健康/就绪、Request ID、首个管理员引导已有单元测试 |

### 7.2 构建与验证状态

在 `backend/` 执行：

```bash
go test ./...
```

本轮修复结果：
当前已完成：

1. `go.mod` 已声明 Store 使用的 XORM PostgreSQL 驱动（`xorm.io/xorm` + `github.com/lib/pq`），且模块依赖可在 `-mod=readonly` 下解析；
2. `OpenAIAdapter.parseSSE` 已实现，支持 Chat Completions、Responses、Usage、`[DONE]`、大事件和回调错误；
3. 已补齐请求字段、多模态图片 URL/Base64、工具参数和 Usage 详情解析；
4. 已增加 Adapter 单元测试。

验证命令：

```bash
go test -mod=readonly ./...
go test -race ./...
go vet -mod=readonly ./...
```

以上命令当前均通过。

## 8. OpenAI Adapter 当前问题分析

文件：`backend/internal/adapter/openai.go`

### 8.1 已存在能力

- OpenAI-compatible Chat Completions 非流式请求；
- Responses 非流式请求；
- Chat/Responses 流式 HTTP 请求入口；
- `/models` 模型发现；
- 统一 Message 和 Tool 到 OpenAI 格式的转换；
- HTTP 错误分类；
- Usage 和非流式 Message 解析。

### 8.2 已完成的 SSE 与 Payload 修复

两个流式入口都会调用：

```go
return a.parseSSE(resp.Body, emit)
```

当前 `parseSSE` 已实现：

- 使用带 16 MiB 单行上限的 `bufio.Scanner` 读取 SSE；
- 正确处理空行、注释行、`event` 字段、多行 `data` 和 EOF 结束事件；
- 识别 `data: [DONE]` 并发出 `done` 事件；
- 解析 Chat Completions 的 `choices[].delta`、`role`、`content`、`tool_calls` 和 `finish_reason`；
- 解析 Chat/Responses Usage（包含嵌套 cached/reasoning details）；
- 保留 Responses 原始事件，同时将文本 delta 归一化为 `StreamEvent`；
- 归一化 Chat 分片的 `choices[].index` 与响应 ID，以及 Responses 的 `output_index`、响应 ID 和显式事件类型；
- 将 Responses 的 `function_call_arguments` 增量与 `output_item.added` 中的 function call 映射为带索引的 `ToolCallDelta`；
- 立即返回 `emit` 回调错误并检查 Scanner 读取错误；
- 流式请求发送 `Accept: text/event-stream`；
- 补齐 `reasoning_effort`、`user`、`previous_response_id`、图片 URL/Base64/Media Type 和工具参数映射。

### 8.3 其他需要修正或确认的点

1. Responses API 的 `Input` 仍是 `json.RawMessage` 并原样透传，但新增了 `protocol.NormalizeResponsesInput`：字符串与消息数组会归一化为 `ResponsesRequest.Messages`，结构非法的 `input`（数字、对象、数组元素既非字符串也非对象、空字符串）在 `httpapi.decodeResponsesRequest` 阶段即被拒绝并返回 400；数组中的 `null` 与空白元素不携带任何内容，被忽略而非拒绝。无法建模的条目（`input_image`、`reasoning` 等）不参与归一化但保留在 `Input` 中，因此同协议透传不丢字段。
2. Responses 的函数调用参数增量已映射到 `protocol.ToolCallDelta`（带 `Index`/`ID`/`Name`/`Arguments`），只有 `response.function_call_arguments.delta` 被视为参数增量，`response.output_item.added` 中的 function call 只先暴露调用 ID 与函数名（`response.output_item.done` 会重复完整参数，映射它会重复计数）；原始事件仍保留在 `Raw`。
3. `StreamEvent` 已增加 `Index`（Chat 的 choice index / Responses 的 output index）、`ResponseID`、`Event`（显式 Responses 事件类型）与 `ToolCalls`，可据此做跨供应商统一编排；未知事件类型继续通过 `Raw` 透传。
4. 新增 HTTP 契约测试覆盖 `input` 两种形态、非法 `input` 的 400、工具参数增量分片与上游 function-call 事件的透传。供应商连接验证（`/providers/{id}/discover|verify`）与流式客户端断开专项测试均已落地（`adapter/native_test.go`、`adapter/sse_test.go`、`gateway/native_test.go`）；多实例共享协调为明确的 V1 非目标，进程内策略状态已收敛到可替换的 `policy.CounterStore` 接缝。逐项判定与证据见 [`docs/ACCEPTANCE_GAPS.md`](ACCEPTANCE_GAPS.md) §7、§8（G11）。
## 9. 数据库与代码一致性观察

数据库初始迁移与当前领域模型整体一致，运行时已覆盖配置、请求、账本、聚合和会话表：

1. Store 通过 XORM Engine + `lib/pq` 访问 PostgreSQL，业务数据访问不直接持有驱动连接池；
2. `requests.user_id`、`api_key_id` 当前没有外键，保留历史数据的策略已通过 schema 设计体现；
3. `attempts.provider_id` 和账本中的关联 ID 也未设置外键，便于 Provider 停用或删除后保留历史；
4. `content_log` 已提供加密写入、读取和管理员解密查询；
5. `aggregates` 已提供小时/天幂等刷新逻辑，由单体维护任务调用；
6. 预算查询直接扫描 NUMERIC 到 Decimal，已由 Store 类型签名约束，生产环境仍应保留数据库集成测试；
7. PriceVersion 依赖调用方生成递增 `version`，管理 API 要求显式传入版本号；
8. 删除 Provider、Alias 或 User 对历史统计和配置的影响采用停用优先策略，生产管理端应继续避免物理删除；
9. 当前工作区初始文件均为 Git 未跟踪文件，提交基线由交付方在集成阶段建立。

## 10. 推荐实施顺序

### 阶段 1：恢复可构建基线

1. ✅ 为 `go.mod` 添加 XORM、PostgreSQL 驱动依赖并整理 `go.sum`；
2. ✅ 完成 OpenAI Chat SSE 解析；
3. ✅ 补充 Responses SSE 解析；
4. ✅ 修正图像字段映射和缺失 Payload；
5. ✅ 为协议转换、Usage、SSE、错误分类编写单元测试；
6. ✅ 确保 `go test ./...`、`go test -race ./...` 和 `go vet ./...` 通过。

### 阶段 2：建立可运行后端

1. ✅ 增加 `cmd/server/main.go`；
2. ✅ 初始化配置、Cipher、Store 和数据库迁移；
3. ✅ 实现健康检查和数据库就绪检查；
4. ✅ 实现首次启动管理员初始化（密码使用 Argon2id）；
5. ✅ 实现后台 Session 登录与管理员初始化后的登录流程；
6. ✅ 实现网关 API Key Bearer 中间件、过期/停用校验和模型白名单辅助函数；
7. ✅ 实现基础统一 Request ID 响应头；
8. ✅ 补充统一 API 错误响应结构。

### 阶段 3：打通核心网关链路
1. ✅ Chat Completions Handler；
2. ✅ Responses Handler；
3. ✅ Models Handler；
4. ✅ 模型别名解析；
5. ✅ Provider Adapter 工厂；
6. ✅ 有序路由与故障转移；
7. ✅ 流式代理和客户端断开处理；
8. ✅ Request/Attempt 记录、Cost/Credits 结算与最佳努力异步账本写入。

### 阶段 4：计费、预算与限流
1. ✅ Usage 规范化和缺失用量估算；
2. ✅ PriceVersion 管理；
3. ✅ Cost/Credits 计算；
4. ✅ 不可变 Ledger 写入；
5. ✅ 单实例滚动预算检查与并发串行化；
6. ✅ RPM/TPM/Concurrency 限流；
7. ✅ 失败尝试成本与成功请求积分规则测试。

### 阶段 5：管理 API 与统计

1. ✅ 用户与 API Key 管理 API；
2. ✅ Provider、模型发现和模型能力管理 API；
3. ✅ Alias 与 Route 配置 API；
4. ✅ 价格、预算、限流配置 API；
5. ✅ 请求日志、Attempt 和可选 Content Log 查询 API；
6. ✅ 管理变更审计日志；
7. ✅ 小时/天聚合维护任务；
8. ✅ Dashboard Stats 查询 API。

### 阶段 6：Vue 3 前端与单体部署

1. ✅ 初始化 Vue 3 + TypeScript + Vite 工程；
2. ✅ 登录和基础布局；
3. ✅ Dashboard、资源导航和基础统计卡片；
4. ✅ Provider/模型/路由管理入口；
5. ✅ 用户/API Key/预算/限流管理入口和创建表单；
6. ✅ 请求与审计日志查询入口；
7. ✅ Docker 构建时嵌入前端产物到 Go；
8. ✅ Dockerfile、Compose、运行配置和部署文档。

## 11. V1 验收标准建议

### 网关能力

- OpenAI SDK 能通过网关调用 Chat Completions；
- 支持非流式和流式文本；
- 支持 Tool Calling；
- 至少支持 OpenAI-compatible 供应商；
- 模型别名可配置多个有序上游；
- 在连接失败、超时、429、5xx 时按规则故障转移；
- 返回稳定的 OpenAI 风格错误结构和 Request ID。

### 管理能力

- 管理员可以登录；
- 可以管理用户和网关 API Key；
- 可以配置 Provider、模型、别名和路由；
- 可以配置价格、预算和限流；
- 可以查看请求明细、Attempt、费用和审计日志。

### 统计与计费

- 每个成功请求有 Request 和 Attempt 数据；Ledger 行要求该模型（或别名）已配置价格版本，未配置时 attempt 记录 `settlement_error=unpriced` 并在 `/api/admin/runtime` 计数，不写零额账本行；
- 多次故障转移的上游成本能够累加；
- 历史账本绑定价格版本；
- 仪表盘能按时间、用户、模型、Provider 过滤；
- 能展示请求量、成功率、Token、Cost、Credits、延迟和 TTFT；
- 预算和限流在并发情况下有确定行为。

### 安全与运维

- 上游密钥不以明文落库；
- 网关 API Key 不可从数据库恢复；
- 内容日志默认关闭；
- 管理操作有审计记录；
- 数据库迁移可重复安全执行；
- `go test ./...` 通过；
- Vue 和 Go 可构建为一个部署产物；
- 支持健康检查和优雅关闭。

## 12. 已确定的 V1 运行决策

1. V1 同时支持 Chat Completions 和 Responses；Responses 状态协议只在 OpenAI-compatible 上游可用，Anthropic/Gemini 提供原生 Chat。
2. Provider 通过接口类型选择 Adapter，允许 OpenAI-compatible、Anthropic 和 Gemini；管理 API 会拒绝未知类型。
3. 对外计费币种固定为 CNY，金额使用 `decimal.Decimal`。
4. Budget 采用硬拒绝：启用窗口达到上游成本上限或积分上限时，新请求返回 429。
5. 每个上游尝试的已报告 Cost 都计入；Credits（积分）只在最终成功请求中计入；客户端中断或失败请求不产生 Credits，但已产生的上游 Cost 保留。统一失败用量规则（`failedAttemptUsage`，四处失败路径共用）：上游回报了用量则采用；只观察到部分输出则按文本估算；两者皆无则不产生用量、因此也不产生 Cost。非流式失败通常走第三条：adapter 不在错误里回报用量，故非流式失败默认无 Cost。
6. Content Log 全局默认关闭，打开后按单条大小截断并 AES-GCM 加密；只有管理员可以通过 API 解密查看，查看动作写入审计。
7. V1 支持多个管理员账号，但统一使用 `admin` 角色，不再细分 RBAC；用户账号使用 `user` 角色。任何启用账号都能登录控制台：`admin` 进入完整管理台，`user` 进入自助视图（`/api/self/**`，只看得到并只能操作自己的资料、密钥与用量——新建/停用/删除自己的密钥、修改自己的密码），`/api/admin/**` 在后端仍只放行 `admin`（其他已登录账号得到 403 `administrator_required`）。
8. V1 按单实例运行，RPM/TPM/Concurrency、预算串行化和维护任务均为进程内协调；扩展多实例时必须替换为共享实现。进程内状态集中在 `internal/policy` 的 `CounterStore` 接口（默认实现为 `NewMemoryCounters()`），唯一替换点是 `policy.NewServiceWithCounters(store, counters)`；把 `CounterStore` 换成共享实现即可在不改网关协议的前提下扩展到多实例。
9. 外部 Prometheus/OpenTelemetry/Webhook 不作为 V1 依赖，后续可在不改变网关协议的情况下增加。

## 13. 结论

当前项目已经形成较清晰的领域模型和数据库骨架，尤其是以下设计方向较完整：

- 按接口类型简化 Provider；
- 模型别名与优先级路由；
- Request/Attempt 分层记录；
- 可扩展 Usage Meter；
- Cost/Credits 双账本与不可变账本；
- 滚动预算和多维限流；
- 敏感凭证加密与内容日志默认关闭。

当前仓库已形成可运行的 Go + PostgreSQL + Vue 管理端基线：支持 Chat/Responses、OpenAI-compatible/Anthropic/Gemini Chat Adapter、Provider/别名路由、顺序故障转移、Request/Attempt、价格结算、Usage Ledger、预算/RPM/TPM/Concurrency 策略、管理员管理 API、统计查询、加密 Content Log 和 Docker 单体打包。后续可在此基线上扩展原生 Provider Responses 状态协议、分位数图表、细粒度 RBAC、登录防护和分布式多实例协调。
