# omni-gateway

单组织 LLM 网关：运行时基于 Go/XORM/PostgreSQL，配套 Vue 3 管理控制台。对外 API 兼容 OpenAI，支持 Chat Completions、Responses、模型别名、有序故障转移、两本账核算（上游成本以元/CNY 计，对外用户消耗以积分/credits 计）、按本人密钥白名单访问模型、限流，以及请求/尝试可观测性。用户组与预算已退出管理 API、控制台与运行期准入；历史数据与账本保留，模型组只用于分类、关联及统计/请求筛选。

## 用 Docker Compose 快速开始

```bash
cp .env.example .env
# 在 .env 中设置 GATEWAY_MASTER_KEY 与 GATEWAY_ADMIN_PASSWORD。
# 生成示例密钥：openssl rand -hex 32
docker compose up --build
```

后端可通过 <http://localhost:8080> 访问：它从 `/` 提供编译好的 Vue 控制台，从 `/v1` 提供 API。PostgreSQL 数据保存在 `postgres_data` 卷中。可选的 Vite 预览容器用以下命令启用：

```bash
docker compose --profile frontend-preview up --build
```

服务端会等待 PostgreSQL 就绪，启动时执行内嵌迁移，并在 `users` 表为空时引导创建第一个管理员。引导密码只在创建第一个用户那一步读取；之后修改该环境变量不会重置已存在的账号。

## 配置

复制 `.env.example` 并填入以下必填值：

- `GATEWAY_MASTER_KEY`：32 字节 AES-256 密钥，以 hex 或 base64 编码；用于加密供应商凭据与可选的内容日志。
- `GATEWAY_ADMIN_PASSWORD`：创建第一个管理员时必填。
- `GATEWAY_ADMIN_USER`：初始用户名（默认 `admin`）。
- `GATEWAY_DB_URL`：PostgreSQL 连接 URL；省略时 Docker Compose 会依据 `POSTGRES_*` 变量推导。

常用可选项包括 `GATEWAY_LISTEN_ADDR`、`GATEWAY_BUSINESS_TZ`、`GATEWAY_CONTENT_LOGGING`、`GATEWAY_CONTENT_LOG_MAX_BYTES`、`GATEWAY_SESSION_TTL`、`GATEWAY_COOKIE_SECURE` 与 `GATEWAY_SHUTDOWN_TIMEOUT`。切勿把 `.env` 或生产密码提交到仓库。

### 主 OIDC Provider（可选）

网关只支持**一个主 OIDC（OpenID Connect）身份源**，由管理员在控制台「设置 → 主 OIDC Provider」维护：`enabled`、issuer、client_id、client_secret、redirect_url、display_name。client_secret 用网关主密钥加密存储、永不回显给浏览器；保存时留空表示保留原值，显式清空请用「清空配置」（置空 + 关闭，记录保留）。

`GATEWAY_OIDC_*` 环境变量**只在首次启动、且配置记录尚不存在时播种一次**，用于从旧的部署方式平滑迁移；播种之后控制台就是唯一事实来源——管理员清空配置后重启不会把环境变量值复活。

- `GATEWAY_OIDC_ISSUER`：IdP 的 issuer URL，必须与 `/.well-known/openid-configuration` 返回的 `issuer` 完全一致。
- `GATEWAY_OIDC_CLIENT_ID` / `GATEWAY_OIDC_CLIENT_SECRET`：在 IdP 注册的客户端凭据，secret 仅由服务端在换取令牌时使用。
- `GATEWAY_OIDC_REDIRECT_URL`：应登记为 `<网关地址>/api/auth/oidc/callback`，生产环境必须为 HTTPS。
- `GATEWAY_OIDC_DISPLAY_NAME`：登录按钮文案，默认「企业账号」。

用该身份源登录时，若绑定的 `issuer + subject` 尚无账号，会自动创建 `role=user` 的普通账号，**绝不按邮箱或用户名合并**到既有账号；这类账号默认不能用密码登录。有效 SSO-only 会话可在账号中心仅填新密码与确认密码开启本地登录，不需要占位原密码；成功后 SSO 与本地密码均可登录，绑定不变、其它会话失效。已有本地密码的账号仍需验证原密码。本项目不提供密码找回，匿名响应不披露账号类型。会话、CSRF 与角色体系沿用密码登录那一套。回调流程为 Authorization Code + PKCE(S256)，state 与 nonce 一次性有效；失败时只跳回 `/?oauth_error=<白名单错误码>`，client secret、access token、refresh token、ID token 都不会返回给浏览器或写入日志。要求 IdP 支持 OIDC 发现与 ID Token（Keycloak、Okta、Entra ID、Google、Auth0、authentik、Dex 等）；只有 OAuth 2.0、没有 OIDC 层的提供方（GitHub classic OAuth App、微信等）不在支持范围内，把其它单点登录系统绑定到主账号的能力本期也未实现。

`GATEWAY_ALLOW_PRIVATE_UPSTREAMS` 默认关闭，只应为可信的开发网络开启；生产环境的供应商 URL 会走网关的 SSRF 感知传输层，该层会拦截私网/链路本地地址、重定向、URL 中的凭据、查询串与片段。`GATEWAY_PREVIOUS_MASTER_KEYS` 接受逗号分隔的旧密钥，用于一次性的凭据/内容重加密。

## API 概览

### 运行时

- `GET /healthz` —— 进程/存活检查。
- `GET /readyz` —— 就绪检查，含数据库可用性。
- `GET /v1/models` —— 需 API 密钥，返回启用的模型别名。
- `POST /v1/chat/completions` —— 需 API 密钥，OpenAI Chat Completions 兼容。
- `POST /v1/responses` —— 需 API 密钥，OpenAI Responses 兼容。
- `POST /v1/messages` —— 需 API 密钥，Anthropic Messages 兼容（可用时走原生透传，否则做纯文本转换）。
- `POST /v1/embeddings` —— 需 API 密钥，OpenAI 兼容 embeddings 透传。
- `POST /v1/images/generations` —— 需 API 密钥，OpenAI 兼容图像生成透传。

调用方使用 `Authorization: Bearer <gateway-api-key>`。密钥可限制到一个模型别名白名单；空白名单表示全部启用别名，不再与历史用户组权限取交集。账号/密钥停用、过期、鉴权及 RPM/TPM/并发限制仍生效。

所有错误共用一个信封：`{"error":{"message":...,"type":...,"code":...,"request_id":...}}`。流式响应中的流内错误事件复用同一结构，因此无论失败发生在流开始之前还是之后，看起来都完全一致。

`POST /v1/responses` 的 `input` 既接受字符串，也接受输入项数组。结构非法的 `input` 会被以 `400 invalid_request_error` 拒绝而不是转发，而网关未建模的字段则原样保留在上游载荷中。
供应商必须显式声明 `supports_responses`，`POST /v1/responses` 才会转发；否则网关直接返回 `501 responses_unsupported`，不会再向上游转发并把上游的 404 报成通用 502。该开关在管理台的供应商表单里是「支持 Responses API」。

### 管理端

会话端点（**任何启用账号都能登录**；`role=admin` 登录后进入完整管理台，`role=user` 登录后进入自助视图）：

- `POST /api/auth/login` —— 密码正确即建立会话；密码错误返回 `401 invalid_credentials`，密码正确但账号已停用返回 `403 account_disabled`。用户名不存在时同样会走一次同代价的密码校验，避免用响应时间区分账号是否存在
- `GET /api/auth/me`
- `POST /api/auth/logout`，需带 `X-CSRF-Token`

`/api/self/` 只操作登录账号**自己**的数据，查询范围来自会话，不能通过查询串中的 `user_id` 放大范围（指定他人密钥 id 一律 `404 not_found`）。密钥创建若伪造他人 `user_id` 明确拒绝，归属修改也明确失败，不静默转移。读取用 `GET`，写操作需 `X-CSRF-Token`（缺失或错误返回 `403 csrf_failed`）：

- `GET /self/summary` —— 账号与实际密码能力、本人用量汇总、限流；无预算字段
- `GET /self/api-keys` —— 本人密钥，只返回掩码（`key_prefix`/`key_suffix`），不返回密钥哈希
- `POST /self/api-keys` —— 为会话本人新建密钥（`name`、`expires_at`、`model_allow`；到期为未来 UTC RFC3339，可省略），明文 `secret` 只在该响应里出现一次，控制台统一使用一次性弹窗，可复制并确认保存，关闭/离开页面/退出后清理，不写浏览器持久存储
- `PATCH /self/api-keys/{id}` —— 本人改名称、到期、白名单或启停；归属不可变
- `DELETE /self/api-keys/{id}` —— 删除自己的密钥，返回 `204`；不转移历史请求
- `GET /self/access?api_key_id=` —— 仅查询所选本人密钥的有效状态、启用模型别名、可确认的协议/能力、定价状态与真实 `first_success_at`；不返回上游地址或凭据，也不调用模型
- `POST /self/password` —— 新密码至少 8 个字符；已有本地密码必须提交正确的 `current_password`，真正 OIDC-bound 的 SSO-only 会话可仅提交 `new_password` 开启本地登录。成功后保留当前会话、注销其它会话
- `GET /self/requests?limit=&offset=&status=&from=&to=&request_id=&api_key_id=` —— 本人安全调用摘要，默认 50 条、最多 200 条；查询一律会话隔离，时间为包含边界的 RFC3339 UTC

管理员资源位于 `/api/admin/` 之下，需要会话 Cookie 且**仅放行 `role=admin`**：其他已登录账号得到 `403 administrator_required`，未登录得到 `401 authentication_required`；写操作还需 CSRF：

- `/users`
- `/api-keys` —— 安全治理列表；任何创建路径均只允许会话本人，管理员对他人密钥仅可查看和 `PATCH {"active":false}`，不能编辑、续期、重启、转移或删除
- `/providers`、`/providers/{id}/models` 与 `/providers/{id}/impact`
- `/aliases`、`/aliases/{id}/routes`、`/aliases/{id}/readiness`，以及别名/关键路由 `/impact`
- `/prices` —— 不可变版本，两账均须显式提供十进制字符串费率；Token 表格单位为每百万 Token，高级 JSON 与 API 为每实际单位
- `/rate-limits`
- `/requests` 与 `/requests/{id}` —— 列表支持精确请求 ID、时间、用户、密钥、模型、协议、供应商、结果、结算状态、模型组筛选及 limit/offset 分页
- `/content/{request-id}`（启用加密内容日志时可用；控制台需单独确认，每次读取留审计）
- `/audit` —— 支持时间、操作者、对象/动作、真实结果、分页；新记录含白名单安全前后摘要，旧记录不伪造补造
- `/stats`
- `/model-groups` —— 仅分类、关联与统计筛选，不再授权用户
- `/runtime`，返回有界的持久化计数器

旧 `/api/admin/user-groups`、`/api/admin/budgets` 路径在正常鉴权后返回 404；旧表、成员关联、预算规则及历史账本不删除。新建别名默认为未启用草稿，新建启用或未启用→启用必须有至少一条启用路由指向存在且启用的供应商。既有异常启用别名只提示，不自动停用；免费合法，未定价只提示、不新增调用拦截。

控制台按任务分组导航，以会话 `me.username` 显示账号入口，管理员和普通用户共用本人密钥、账号与快速接入流程。`#/页面?...` 保留允许的筛选、分页和请求详情，不保存密钥/密码/正文；无权限或已撤除链接会提示并回到允许页面。详情采用桌面侧抽屉、小屏全屏面板，Escape/关闭恢复原行焦点；正文关闭即清理。轮换是本人建新→手动迁移客户端→核对新密钥真实成功记录→手动停旧，不自动调用、签发或迁移历史。

## 本地开发

`make` 封装了常用工作流；执行 `make help` 查看完整列表。

```bash
cp .env.example .env    # 然后设置 GATEWAY_MASTER_KEY 与 GATEWAY_ADMIN_PASSWORD
make run DB_URL='postgres://user@localhost:5432/llm_gateway?sslmode=disable'
```

`make run` 加载 `.env` 并启动后端。`make build` 先构建 Vue 控制台，把 `frontend/dist` 复制进 `backend/internal/httpapi/static`，再构建内嵌该控制台的 Go 二进制——当你需要让本地构建的服务端提供真实管理界面时就用它。直接 `go build` 只会嵌入一个占位页面，因为控制台由前端工具链产出。

Go module 为 `github.com/zjvtit/omni-gateway`；所有内部 import 都使用该路径。

后端细节（需要可访问的 PostgreSQL）：

```bash
cd backend
go test -mod=readonly ./...
go vet -mod=readonly ./...
go build -mod=readonly ./cmd/server
GATEWAY_DB_URL=postgres://... \
GATEWAY_MASTER_KEY=... \
GATEWAY_ADMIN_PASSWORD=... \
go run ./cmd/server
```

前端细节：

```bash
cd frontend
npm ci
npm run dev       # Vite 开发服务器，端口 5173；/api、/v1 与健康检查代理到 :8080（可用 GATEWAY_DEV_URL 覆盖）
npm run build     # 类型检查并产出生产构建
npm run test      # Vitest 单元测试
npm run preview   # 生产预览；Docker 预览会把请求代理到后端
```

本项目所用的远程 Podman 部署见 [`docs/PODMAN_REMOTE.md`](docs/PODMAN_REMOTE.md)；无法访问任何容器镜像仓库的受限主机上的 Docker 部署见 [`docs/DEPLOY_172.md`](docs/DEPLOY_172.md)。两份文档都包含 SSH 连接、远程镜像限制、更新命令与运维清单。
生产环境请把后端镜像作为唯一的部署产物：它的 Docker 构建会编译 Vue 应用并将其嵌入 Go 二进制。把它放在负责 TLS 终止的反向代理之后，设置 `GATEWAY_COOKIE_SECURE=true`，并使用强且唯一的数据库/管理员凭据。

## 测试与 CI

每次提交前都跑完整验证门禁——它与 CI 使用的流程完全一致：

```bash
make test TEST_DATABASE_URL='postgres://user@localhost:5432/postgres?sslmode=disable'
# `make test` 只是 CI 所跑脚本的薄封装：
TEST_DATABASE_URL='postgres://user@localhost:5432/postgres?sslmode=disable' scripts/test.sh
```

该脚本依次执行 `gofmt -l`、`go vet -mod=readonly ./...`、`go build -mod=readonly ./...`、`go test -mod=readonly -race -count=1 ./...`，然后在 `frontend/` 中执行 `npm ci`、`npm run build` 与 `npm run test`（Vitest）。

`TEST_DATABASE_URL` 指向一个临时 PostgreSQL 库。`internal/store` 与产品 HTTP 集成测试会自行创建并删除临时 schema，验证历史数据保留、白名单协议、查询/过滤/计量与密码能力，因此应使用专用隔离空库。缺少该变量时数据库集成测试会自行跳过——脚本会打印醒目的警告后继续，除非传入 `--strict`（或以 `CI=true` 运行），此时缺少该变量即为硬失败；跳过不能记为全量通过。

本轮已批准产品范围、逐项本地验收、真实命令与审阅修复证据见 [`docs/PRODUCT_ACCEPTANCE_2026-10-03.md`](docs/PRODUCT_ACCEPTANCE_2026-10-03.md)；实施方案和 F06 暂缓边界见 [`docs/PRODUCT_CHANGE_PLAN.md`](docs/PRODUCT_CHANGE_PLAN.md)。开发与独立 preview 都代理 `/v1`，复制的同源 Base URL 不会落入控制台 HTML fallback。

`.github/workflows/ci.yml` 在每次推送到 `main` 以及每个 pull request 上运行同一脚本，矩阵覆盖 `postgres:14` 与 `postgres:16` 两个服务容器。两个主版本都必须保持绿灯：`postgres:16` 是 Compose 与远程 Podman 部署实际运行的版本，而 `14` 仍被较老的开发机与部署使用。正是这个矩阵保证了实际部署的主版本始终被验证到。

## 设计说明

- 供应商凭据在静态存储时使用 AES-256-GCM 加密；网关 API 密钥只以 SHA-256 哈希保存。
- 价格版本不可变。成本（上游，元/CNY）对每一次上游尝试都计费；积分（积分，保留三位小数）只对成功的请求计费。当某次尝试有可计费用量却没有匹配的价格版本时，不写入任何账本记录：该尝试以 `settlement_error=unpriced` 保存，并计入 `GET /api/admin/runtime` 的 `unpriced_settlements`，这样"缺价格"会一直可见，而不会变成免费流量。结算与请求/尝试写入走一个有界的重试队列并使用幂等 ID；持久化失败不会改变 API 的返回结果，但会体现在运行时计数器与日志中。
- 限流策略是单进程、确定性的，契合单实例部署模型。进程内策略状态藏在 `internal/policy` 的 `CounterStore` 接口之后（默认 `NewMemoryCounters()`，通过 `policy.NewServiceWithCounters` 注入），多实例部署需要共享计数器/锁。历史预算规则不再参与准入或新增预算消费；实际两账结算与用量统计不停止。
- 内容日志默认关闭，且持久化前会先加密。

### 积分 (credits) 约定

- 积分标定约定：house 默认值为**每输入 token 0.0002 积分、每输出 token 0.0004 积分**（= 0.2 / 0.4 积分每千 token）。e2e 固件（`scripts/e2e/setup.py` 的 `PRICES`）与控制台价格表单默认值都采用它。积分是独立单位：不存在对 CNY 的存储汇率，两本账之间也从不做加减。
- 费率约定：成本与积分费率均允许 **0**，负数和非法数字仍拒绝；积分金额量化到三位小数（`model.CreditsScale = 3`），但费率本身保留精度，因此极小的非零费率可能结算为 `0.000` 积分。
- 创建价格必须显式给出非空的 `cost` 与 `credits` 两张表，且 `credits` 必须覆盖 `cost` 中每个计费项（允许明确填 `"0"`）。只给成本、漏填必要积分费率，或仅发送旧 `sale` 字段，仍会被以 `400` 拒绝。价格版本不可变，调整为免费需创建新版本，不改历史价格或自动补账。
- **免费调用仍完整计量**：管理员可在价格表单点击「设为免费：成本和积分均为 0」，核对后保存零费率版本；该操作不自动保存，也不更改已有模型。正用量仍记录请求/尝试、实际或估算用量及其标记、Token、耗时，并写入含数量和价格版本的零金额账本。没有价格不是免费，仍记为 `unpriced`；不能仅凭零金额判断某次历史请求免费。
- 成本与积分独立：成本非零、积分为零仍记成本账，反之仍记积分账。免费不绕过鉴权、密钥模型白名单、账号/密钥停用与过期或 RPM/TPM/并发限流。预算已撤除，不再因旧预算耗尽或零上限拦截；旧规则及消费历史保留。
- 可观测性：`GET /api/admin/runtime` 暴露 `unpriced_settlements`（有可计费用量但无匹配价格版本）与 `zero_credit_settlements`（缺少必要积分费率，或非零积分费率量化后仅收 `0.000`）。显式零积分费率不计入零积分异常，即使其上游成本非零；两表均未定价的聚合计量项（如 `total`）沿用非计费语义，不重复计入 Token 统计。
- 迁移约定：迁移只向前，且 `schema_migrations` 只记录版本名（无校验和），因此 `0001_init.sql` 保持为不可变历史，`0005_credits.sql` 执行带存在性守卫的 `RENAME COLUMN`（`sale`→`credits`，`sale_limit`→`credit_limit`）。
- 没有反向迁移，也没有 API 兼容层。这是可接受的，因为重构时并不存在线上数据：管理端 JSON 字段被直接重命名，`profit` 从响应中移除。

## 跨会话开发记录

新会话先读根目录 [`AGENTS.md`](AGENTS.md) 和 [`docs/DEVELOPMENT_LOG.md`](docs/DEVELOPMENT_LOG.md) 的最新条目，再对照 Git 基线按需查代码。每次功能或修复结束时，更新日志中的变更、测试证据与遗留项；记录落盘可供本机接续，提交后才能随 clone/pull 传到其他机器。`.pi/` 是被 Git 忽略的本机辅助目录，不作为唯一交接来源；日志不得包含密钥或密码。

## 许可证

MIT —— 见 [`LICENSE`](LICENSE)。Copyright (c) 2026 omni-gateway contributors.

第三方组件仍受其各自许可证约束；依赖清单见 `backend/go.mod` 与 `frontend/package.json`。
