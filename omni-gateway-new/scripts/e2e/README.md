# 端到端测试脚本集

针对网关数据面与策略引擎的可复现端到端检查。这些脚本以客户端的方式通过 HTTP
驱动一个**正在运行**的网关——不使用任何进程内测试替身——它们正是
[`docs/E2E_TEST_REPORT.md`](../../docs/E2E_TEST_REPORT.md) 中判定表的产出工具。

Go 测试套件（`scripts/test.sh`）仍是 CI 门禁。本脚本集是更丰富、协议级的补充：
没有数据库、主密钥和 mock 上游，CI 无法运行它。

## 目录结构

| 文件 | 用途 |
| --- | --- |
| `lib.py` | 共享客户端：管理 API（会话 Cookie + `X-CSRF-Token`）与数据面（网关 API 密钥）。 |
| `mock_upstream.py` | 无依赖的 mock 上游。`MODELS` 中含一个会 sleep 的 `*-slow` 变体，以及用于失败路径的 `broken-model` / `unknown-model`；仅 `user=compat-fixture` 的 Chat 请求启用兼容性响应。 |
| `setup.py` | 幂等地创建供应商、别名、路由、价格、用户、密钥与限流规则；写入 `fixtures.json`。 |
| `native_matrix.py` | 原生协议矩阵：embeddings / images / responses / messages，包含协议转换与不支持操作的判定。 |
| `openai_compat_matrix.py` | 真实 HTTP OpenAI Chat 兼容矩阵：非流式与 SSE 的推理、工具调用、用量和别名保真。 |
| `policy_matrix.py` | 策略拦截矩阵：rpm、tpm、并发与预算，逐项断言 HTTP 判定。 |
| `run.sh` | 一条命令跑完：mock 上游 + setup + 三个矩阵。 |
| `fixtures.json` | **已被 gitignore。** 由 `setup.py` 原子写入（文件权限 0600）的 id 与真实 API 密钥；stdout 不输出密钥。 |

## 前置条件

1. **一个数据库。** 脚本集会写入真实数据行，所以请给它一个一次性数据库：

   ```sh
   createdb llm_gateway_e2e
   ```

2. **一个从当前代码树构建、并监听在脚本集端口上的网关。** 以下检查假定
   `127.0.0.1:18090`：

   ```sh
   make build
   cd backend && GATEWAY_LISTEN_ADDR=127.0.0.1:18090 \
     GATEWAY_DB_URL='postgres://localhost:5432/llm_gateway_e2e?sslmode=disable' \
     GATEWAY_MASTER_KEY="$(openssl rand -hex 32)" \
     GATEWAY_ADMIN_PASSWORD='E2eAdmin!2345' \
     GATEWAY_ADMIN_USER=admin \
     GATEWAY_ALLOW_PRIVATE_UPSTREAMS=true \
     GATEWAY_CONTENT_LOGGING=true \
     ./server
   ```

   `GATEWAY_ALLOW_PRIVATE_UPSTREAMS=true` 是必需的，因为 mock 上游在回环地址上；
   `GATEWAY_CONTENT_LOGGING=true` 则是让报告中的内容日志断言有意义的前提。

3. **Python 3。** 不使用任何第三方包。

## 运行

```sh
scripts/e2e/run.sh                          # mock 上游 + setup + 三个矩阵
scripts/e2e/run.sh --no-mock                # 复用已在运行的 mock 上游
python3 scripts/e2e/openai_compat_matrix.py # 单独跑兼容矩阵（先运行 setup.py）
python3 scripts/e2e/policy_matrix.py        # 单独跑策略矩阵
```

`run.sh` 一旦遇到判定不匹配就立刻以非零状态退出，因此可以安全地串进脚本里。

### 环境变量

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `E2E_GATEWAY` | `http://127.0.0.1:18090` | 网关基础 URL。 |
| `E2E_ADMIN_USER` | `admin` | 管理员用户名。 |
| `E2E_ADMIN_PASSWORD` | `E2eAdmin!2345` | 管理员密码；必须与网关的 `GATEWAY_ADMIN_PASSWORD` 一致。 |
| `E2E_FIXTURES` | `scripts/e2e/fixtures.json` | 读写 id 与 API 密钥的位置。 |
| `E2E_MOCK_PORT` | `19099` | mock 上游端口。 |
| `E2E_FRESH_WAIT` | `62` | `policy_matrix.py` 等待新计费窗口的秒数。 |

### 运行顺序

`setup.py` 必须先于任一矩阵运行：`lib.py` 读取的 API 密钥正是由它写入的。
`native_matrix.py`、`openai_compat_matrix.py` 与 `policy_matrix.py` 相互独立；
`run.sh` 将兼容矩阵置于策略矩阵之前。

## 固件

`setup.py` 是幂等的——重复运行会复用已有的供应商、别名与价格，但会**删除并重新签发**
`e2e-key-*`，因此 `fixtures.json` 里始终是有效密钥。注意这会重置这些密钥此前累积的状态。
复用已有供应商时，`setup.py` 会把 `supports_responses` 对齐到当前固件（mock 上游实现了
`/v1/responses`）；否则在 0007 之前建的库上，Responses 用例会收到 501 `responses_unsupported`。

别名，每个场景一个：

| 别名 | 路由 | 用途 |
| --- | --- | --- |
| `mock-chat` | OpenAI `mock-gpt-4o` | 常规对话、账本检查。 |
| `mock-embed` | OpenAI `mock-gpt-4o` | `/v1/embeddings`。 |
| `mock-image` | OpenAI `mock-gpt-4o` | `/v1/images/generations`；按价格版本 v2 的 `image` 计费项计费。 |
| `mock-responses` | OpenAI `mock-gpt-4o` | `/v1/responses`。 |
| `mock-translate` | OpenAI `mock-gpt-4o` | 把 `/v1/messages` 转换到 OpenAI 供应商。 |
| `mock-slow` | OpenAI `mock-gpt-4o-slow` | 并发拒绝（2 秒上游）。 |
| `mock-rpm` / `mock-budget` | OpenAI `mock-gpt-4o` | 限流与预算拒绝。 |
| `mock-unpriced` | OpenAI `mock-gpt-4o-mini` | **刻意没有**价格版本：用于复现 `unpriced` 结算路径。 |
| `mock-claude` | Anthropic `mock-claude` | `/v1/messages` 上的 Anthropic 原生透传。 |
| `mock-unsupported` | Anthropic `mock-claude` | Anthropic 路由上的 `/v1/embeddings` → 501 `operation_unsupported`。 |

密钥：`e2e-key-chat`（无规则）、`e2e-key-rpm`（rpm 2）、`e2e-key-conc`（并发 1）、
`e2e-key-tpm`（tpm 1）。

### OpenAI Chat 兼容固件

兼容矩阵通过 `mock-chat` 别名和 `e2e-key-chat` 发送 `user=compat-fixture`，仅此标记会让
mock Chat 返回专用非流式 / SSE 响应，普通请求的既有响应不变。两种请求均携带
`max_completion_tokens`；mock 特殊分支若收到 `max_tokens` 替代此键便返回 HTTP 400，
因此矩阵的 HTTP 200 判定也验证了请求字段名到达上游未被改写。矩阵断言非流式
`created` / `system_fingerprint` / `reasoning_content` / `logprobs`，以及 SSE 上游 ID、
时间戳、推理分片、两个并行工具调用、另一 choice、推理用量和恰好一次 `[DONE]`，
并检查客户端响应中的模型名仍为 `mock-chat`。该矩阵只输出判定，不打印响应体、
API 密钥或提示词。
`setup.py` 只打印新密钥的名称与 ID，密钥值仅写入指定的固件文件；请仍将该文件视为敏感材料，验收后删除。

## 约定

* **金额字段是十进制字符串。** 管理 API 载荷必须把 `cost_limit`（元/CNY）/
  `credit_limit`（积分）/ 价格值以字符串发送，例如 `"1.00"` 或 `"0.004"`。
  JSON 数字会被以 HTTP 400 拒绝。一个价格版本同时携带两本账：`cost` 是每单位
  多少元，`credits` 是每单位多少积分。积分*金额*会量化到三位小数，因此积分费率
  必须足够精确，使单个可计费单位不会四舍五入为零。
* **策略规则按作用域存储，并由 `setup.py` 重置。** `policy_matrix.py` 在预算用例
  之前会停用所有预算规则，因此它可重复运行且与顺序无关。
* **判定对顺序敏感。** 并发用例断言的是判定的*集合*而非顺序：两个并行调用谁先
  完成都可能。
* **API 密钥只能读一次。** 网关只返回一次密钥；这正是 `fixtures.json` 存在的
  原因，也是它被 gitignore 的原因。
