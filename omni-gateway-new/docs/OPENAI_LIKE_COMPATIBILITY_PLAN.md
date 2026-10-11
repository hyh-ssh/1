# OpenAI-like 接口兼容性提升计划

基线：`feat/oidc-sso-login` 上的 `5cfb93a`（Responses 需要供应商显式 opt-in，默认拒绝）。对标材料见 [AI_GATEWAY_BENCHMARK.md](AI_GATEWAY_BENCHMARK.md)；其中 ai-gateway 的行为来自 v1.0.1 二进制恢复和干净室实现，不等于官方契约。LiteLLM 可作为供应商能力表、协议转换和测试矩阵的设计参考，但本轮不把未经核实的 LiteLLM 实现细节当作断言，也不引入其运行时。

设计取舍有静态代码依据：ai-gateway 重建实现的 `reconstruction/internal/httpserver/conversion_forward.go` 让原生 plan 先于显式启用的转换 plan，原生响应走 `server.go` 的透传路径；LiteLLM 的 `streaming_handler.py` 在 `CustomStreamWrapper` 中保留 `delta.reasoning_content`、处理 usage-only chunk。LiteLLM 引用来自所读快照 `cddc5346`，未运行其实例，路径与行为不推及所有供应商。我们保留本项目对请求/响应的安全重建，而非照抄字节透传。

## 范围、团队与交付顺序

| 顺序 | 负责人（工作流） | 交付 | 验收 |
|---|---|---|---|
| C0 | 集成负责人 | 提交已完成的 Responses opt-in 与对标基线；维护本计划和变更边界 | 工作区基线干净；501 `responses_unsupported`、上游尝试为 0；无 Responses→Chat 回退 |
| C1 | 协议适配成员 | OpenAI Chat 请求键名、响应和 SSE 归一化：`reasoning_content`、工具索引、choice 索引、created、logprobs、fingerprint | adapter 单测覆盖 tool `index=0/1`、`max_completion_tokens` 不改名、usage 不回退 |
| C2 | HTTP 出站成员 | Chat 解码与 SSE/非流式输出保真；保持模型别名、账务和错误信封 | handler 单测覆盖推理字段、并行工具增量、非零 choice index、上游 id/时间/usage/DONE；未声明 Responses 的 501 回归 |
| C3 | 跨协议成员 | Anthropic thinking/tool index/cache 与 Gemini thought/usage、工具调用/结果和受控 `tool_choice` 映射 | mock 流式和非流式用例；多工具 ID/index 可回传、未知工具与无法安全关联的结果在本地拒绝；不凭空生成上游未产生的推理内容 |
| C4 | 集成/审查负责人 | 跨层 mock 上游验证、竞态/构建、读码复审、逐切片提交 | Go 测试、前端回归、真实 PG 迁移、HTTP E2E；差异与仍未覆盖项公开记录 |

执行边界：各成员不重叠编辑文件；先提交基线，再交付 C1–C3，C4 按真实测试结果收口。优先复用已有 `protocol.StreamEvent`，保持 route、budget/settlement、SSRF、auth 及既有 Responses 501 行为。默认不要转发未知的客户端鉴权/逐跳头；不能因为追求原始字节透传而绕过代理安全检查。

## 兼容性决策

- **OpenAI-compatible 原生优先**：相同协议尽可能保留已验证的字段及 SSE 语义，同时仍要改写外显模型别名并按归一化用量结算。原生透传与跨协议转换必须分别测试；以后若扩展路由矩阵，应将原生候选优先和转换 opt-in 写入调度契约。
- **Responses 是独立协议**：只有供应商声明 `supports_responses` 时转发 `/v1/responses`。不支持时返回 501，零上游调用；绝不悄悄变成 Chat。
- **推理可见性分层**：仅转发上游实际提供的 `reasoning_content`/thinking/thought 和 usage 的 reasoning token 计数；这不是补写模型内部思维链。子代理 `Task/TaskWait` 的运行状态展示不在网关仓库中，缺少中途报告不能作为网关丢字段的证据。
- **未知 400 不猜测**：历史 PI 流式 Chat 的具体校验错误缺少匹配请求的脱敏上游响应。`max_completion_tokens` 被改写是代码可证明的兼容性缺陷，但并未证明它造成那 11 次历史 400。后续仅在可控 mock/授权复现中比较参数名和请求结构，不记录完整提示词或上游错误体。
- **账务不变式**：跨协议新增 cache/reasoning meter 时，对比原有 input/output/total 口径和价格匹配；累计上游计数先差分再进入增量流，不重复计数；若某上游未返回对应字段，不杜撰可计费用量。

## 本轮测试矩阵

| 场景 | 必要断言 |
|---|---|
| OpenAI Chat 非流式 | `reasoning_content`、`created`、`system_fingerprint`、`logprobs`、外显 alias 与 usage 保留 |
| OpenAI Chat SSE | 分片 `reasoning_content`、两个并行工具 `index=0/1`、choice index、id/created 连贯；usage 帧、`[DONE]` 仅一次 |
| 请求兼容 | 只传 `max_completion_tokens` 时上游看到同名键而非 `max_tokens`；只传后者行为不变；两个字段同时出现时不静默吞掉其一 |
| Anthropic/Gemini 转 Chat | 仅映射实际可见 thinking/thought，计量细节可查；多工具 ID/index 可回传，Anthropic 并行 `tool_result` 合并为同一 user turn，未知、重复或非紧邻的工具结果 ID 在本地拒绝；Gemini 未知工具结果 ID 本地拒绝，`STOP` 中含工具调用时返回 `tool_calls`，图片 data URL 转为 `inlineData`；非文本工具结果不被静默丢弃而是本地拒绝；`tool_choice` 只映射供应商支持的形状 |
| Responses 回归 | 未 opt-in：同步/流式都 501，零上游尝试；已 opt-in：原生 Responses SSE 可用，不回退 Chat |
| 本地协议拒绝与安全 | 不可表达的工具选择、无法关联的工具结果及跨协议保留字段覆盖在上游调用前拒绝；HTTP 400 与请求记录一致，错误不回显调用 ID/函数名/原始 JSON；真实上游 400 仍单独分类 |
| 安全/可靠性 | 并发/竞态测试、无密钥/提示词/原始错误体输出；旧预算、账务、SSRF 和认证测试通过 |

## 后续候选（非本轮完成声明）

1. 复现并定位历史流式 Chat 400：需要**同一请求**的脱敏参数名、错误码和上游校验结果；拿不到证据则保持“未确定”。
2. 协议能力矩阵与原生路由优先：为供应商建立可验证的 endpoint/stream/tool/reasoning 能力项；任何跨协议转换默认关闭，逐能力 opt-in。
3. 有界并发等待和在飞指标、日志保留清理、调度与运行期配置：见对标文档 W1–W17，分别评估运维收益、迁移和账务边界后实施，不能由端点数量推导优先级。
4. 缓存与推理 token 的独立定价：本轮只归一化上游已报告的补充 meter，未改变计价器按 meter 逐项计价的规则。特别是供应商将缓存包含在 `input` 中时，若同时配置 `input` 与 `cache_input` 费率，需另行定义互斥数量或折扣规则并做账本验收；本轮的 13 项 HTTP 判定不覆盖该价格组合。
5. 浏览器端 `Task/TaskWait` 中途状态：先确认具体宿主实现与可观测事件契约；网关只能保证模型提供的字段被转发，无法独自实现子代理进度流。
6. Anthropic 连续普通 `user` 回合目前不合并；是否需要合并应先依据真实供应商行为验证，而非假定其角色交替限制。非规范角色携带 `tool_calls` 的本地校验仍可收紧。Gemini 无 ID 的同名工具调用跨帧无法可靠判断属于同一调用，暂分别分配 ID/index；若上游确会分帧发送一个无 ID 调用，需补充可验证的关联契约，不能仅按函数名合并。

## 本轮交付与验收记录

- C0 已由 `5cfb93a`（Responses opt-in）和 `e727336`（对标基线）提交；本轮 C1–C4 已完成 OpenAI Chat 字段保真、HTTP/SSE 出站、Anthropic/Gemini 可见推理与工具映射、本地脱敏拒绝及跨层矩阵，按协议、HTTP、E2E 和文档分片提交。独立只读复审未发现新增阻断问题。
- **最终代码**通过 `gofmt -l .`、`git diff --check`、`go test ./... -count=1`、`go build ./...`、`go vet ./...` 和 `go test -race ./... -count=1`（后端 **13/13 包**通过）；E2E Python 编译及 `bash -n` 通过。前端在本轮后端最终修补前通过构建及 **81/81** 测试，之后未改前端。真实 PostgreSQL 14 临时集群中迁移相关测试 **5/5**、七个内嵌迁移实际启动与二次启动幂等此前通过；最后的适配器修补未改迁移。PostgreSQL 16 未实测。
- **最终代码**重新构建的网关在 scratch 内全新 PostgreSQL 14、隔离端口与 mock 上游运行 `scripts/e2e/run.sh`：native **6/6**、OpenAI-compatible Chat **2/2**、policy **5/5**，合计 **13/13**。已核对网关进程使用新构建二进制、隔离数据库路由指向该 mock；临时凭据/固件为 0600，验收后停止临时进程并删除数据库、凭据和固件，既有 5432/18090 未触碰。native 矩阵的 Responses 200 仅验证**显式 opt-in 的 mock**；未 opt-in 的 501/零上游调用由既有单测和先前独立 E2E 覆盖。
- 这些修复不等于历史 PI 流式 Chat 400 的因果定位：对应 11 次请求缺少匹配的脱敏上游校验结果，精确触发字段仍未知。模型 reasoning 字段不是子代理中途进度；宿主 `Task/TaskWait` 的进度契约仍待另行核查。

未实现的候选事项不算已完成；本计划以实现提交和上述可复核测试为准。
