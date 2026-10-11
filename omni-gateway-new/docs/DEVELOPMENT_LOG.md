# 开发日志（DEVELOPMENT_LOG）

本文件是跨会话的工作记忆：落盘后本机新会话即可读取；提交进 Git 后才会随 clone/pull 传到其他机器。`.pi/` 是已 gitignore 的本机辅助目录，不作为唯一交接来源。新条目放在顶部，记录结论、路径与证据，不复制代码或凭据；`README.md` 描述当前能力，本文件记录「最近改了什么、怎么验证、还差什么」。

## 2026-10-03 — 产品交付推送 main 并部署到 172 Docker 环境

- 摘要：用户授权「push代码后部署」，并在两套目标中明确选择 `root@172.20.80.130` 的 Docker 受限主机。批准交付提交 `100799f` 已推送功能分支，随后在独立发布 checkout 将 `main` 快进并推送，应用已上线；本条为发布后的文档留痕，不增加运行代码变更。
- 代码基线：线上应用产物对应 `100799f45516b6567e92917aa1e1117f7749c82b`。原工作区仍为 `feat/admin-oidc-config`；旧会话占用的 `main` 工作树未改动。使用本会话 scratch 的独立 `release-main` checkout，部署前重新 fetch 并验证干净 `main`、`HEAD == origin/main`；没有 force-push 或启用 `SKIP_BRANCH_GUARD` / `ALLOW_UNPUSHED_MAIN` / `FORCE_NEW_SECRETS`。
- 改动文件：本次应用提交包含全部既有及获批交付的 95 个文件（功能、稳定回归、README、计划/评估/验收与交接文档）；发布后仅新增本日志条目。`.env`、fixture、日志和 scratch 均未提交，暂存范围及敏感模式扫描通过。
- 提交前实跑：test-runner 重启专用回环 PostgreSQL 14.24 后执行 `make test TEST_DATABASE_URL="$TEST_DATABASE_URL"`，退出 0；gofmt、vet、Go build、race、npm ci、vue-tsc/Vite 与 Vitest 全通过。另执行 `cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -mod=readonly -race -count=1 -json ./...`，主代理独立解析为 13 包、421 顶层/940 含子测试、store 46，0 失败、0 跳过；前端 18 文件/301 项通过。发布后文档留痕提交前再次执行完整 `make test`，退出 0，13 Go 包与 301 前端项通过，数据库集成再次真实运行。非沿用上轮结果；npm install-scripts 与 jsdom scrollTo 提示为非阻断。最终只读 code-reviewer 复核无中高风险或阻断。
- 备份：部署前在目标机 `/opt/omni-gateway-backups/pre-product-release-20261003-181853` 保存 PostgreSQL custom 归档、旧二进制/镜像 ID、Compose/运行文件与原配置。数据库归档 160653 字节，`pg_restore --list` 与 SHA-256 校验通过；目录权限 0700、配置 0600，凭据/归档未回显或入 Git。备份保留于目标机，不创建新回滚系统。
- 可复现部署命令：在干净同步的 `main` 运行 `HOST=root@172.20.80.130 PUBLIC_URL=http://172.20.80.130:8080 CONTAINER_CONNECTION=podman-machine-default scripts/deploy-docker-restricted.sh`；本轮 `TMPDIR`、`STACK_ENV` 和仅记录二进制校验和的 SCP 包装器均指向会话 scratch。脚本采用目标机已有配置、复用既有基础镜像与 PostgreSQL 卷，构建/上传/Compose 更新/readyz 均完成，退出 0。
- 目标验收：`docker compose ps` 两容器 healthy，backend 重启次数 0；PostgreSQL 未重建，启动时间仍为 2026-09-20。上传文件及运行容器的二进制 SHA-256 均为 `f172e37a8db7b812707ee94835994c0210f4d31d8bf7dbfae3eb188ebcc97a17`。PostgreSQL 16 上已应用前向迁移 `0010_product_observability.sql`，新增 attempt 状态和 4 个审计列；这不冒称重新运行了 PG16 全量集成测试。
- 数据/配置保留：原配置逐字节一致（仅按既有脚本过滤原生部署 DB/listen 覆盖），权限仍 0600；卷仍为 `omni-gateway_postgres_data`。16 张关键表的计数与全行摘要前后完全一致，涵盖用户、OIDC、密钥、历史用户组/关联/预算规则、供应商/模型/路由、价格、模型组与限流；固定部署前边界内的历史 usage_ledger 摘要完全一致，无删除、转移、补账或密钥轮换。
- 实际访问验证：`http://172.20.80.130:8080` 与 `https://omni.zjic.org` 的 `/healthz`、`/readyz` 均 200/ok；HTML、JS、CSS 与本次构建 SHA-256 精确匹配。匿名 `/api/auth/me` 和 `/v1/models` 返回 JSON 401。Ego Browser 在两个地址实际渲染登录页、加载新资源且无横向溢出，专用空间已关闭；未登录生产账号、签发密钥、读取正文或调用生产模型。
- 中间问题：SSH 流式备份脚本首次被 `docker compose exec -T` 消耗后续输入，仅导出归档而未完成校验；改为上传脚本文件再执行，补齐归档列表和前后摘要。手动门禁预检首次缺 `ROOT_DIR`，补齐后通过，正式脚本门禁也通过。未将这两次预检当成功；正式部署无失败。GitHub Actions 匿名 API 返回 404，本机无 gh，远端 CI 状态未确认，不宣称其绿灯。
- 证据：会话 scratch 的 `release-make-test.log`、`release-backend-tests.jsonl`、`release-test-summary.json`、`release-docs-make-test.log`、`release-backup.log`、`release-deploy.log`、`release-remote-verify.log`、`release-http-evidence.json`、`release-browser-evidence.json`、`release-cleanup-evidence.json`。本条只记安全结果，不含密码、密钥或完整用户请求。
- 清理：专用本地 PostgreSQL 已停止，60963–60967 无监听；临时取回的生产配置副本和本会话部署临时目录/Node 缓存已删除，浏览器空间已关闭。测试数据目录未删除，目标机配置、数据库卷与受保护备份保留。
- 遗留/范围：本次推送与部署无剩余阻断；F06 继续按批准范围暂缓。生产只做健康、资源、迁移与数据保留检查，不冒称完成真实生产付费转发或管理员/SSO 端到端验收；GitHub 远端 CI 状态未能确认。

## 2026-10-03 — 已批准产品计划本地实施、验收与清理完成

- 摘要：获批 F01–F05、F07–F13 已实现并通过本轮 13/13 条验收；F06 随预算撤除暂缓。用户组/预算退出界面、API 和运行期，历史数据保留，模型组只用于分类/关联/筛选。空密钥白名单访问全部启用别名的扩张已明确接受，其余鉴权、启停/过期、限流、角色和正文边界保留。
- 代码基线：`6ccfb89`，分支 `feat/admin-oidc-config`，保留接手时未提交免费计量及其它既有变更。本轮没有新增提交、推送、合并、部署或生产访问；成果仅在本地工作树。
- 分工：后端统一写入者；Operations 模型/诊断/审计 UI；PriceEditor/priceMath；PersonalPane/账号密钥接入；主代理 App/api/navigation/styles/Dashboard 集成与 Vite 接入代理修复；整壳测试迁移、证据核对及独立只读审阅分别委派。补充覆盖中断后由接续代理保留已落盘测试完成，无热点并发覆盖。
- 改动文件：`backend/internal/{httpapi,store,policy,gateway,billing,model}` 的撤除/归属治理、密码/个人接入、就绪/影响、筛选/安全审计及对应回归；前向新增 `store/migrations/0010_product_observability.sql`，旧迁移不改。新增精度/旧快照/协议/审计/密码 follow-up 测试覆盖。`frontend/src/{App.vue,api.ts,navigation.ts,styles.css,DashboardPane.vue,PersonalPane.vue,PersonalDialog.vue,personalConsole.ts,PriceEditor.vue,PriceList.vue,priceMath.ts,OperationsPane.vue,OperationsRequestDrawer.vue}` 及相应 specs，另有 `frontend/vite.config.ts`/`src/viteProxy.spec.ts`。README、计划与验收文档同步。
- 可复现门禁：准备专用本地 PostgreSQL，设置隔离库 `TEST_DATABASE_URL` 后执行 `make test TEST_DATABASE_URL="$TEST_DATABASE_URL"`；它运行 gofmt、vet、Go build、`go test -mod=readonly -race -count=1 ./...`、npm ci、前端类型检查/build/Vitest。最后主代理门禁退出 0；PostgreSQL 14.24 的数据库集成真实运行，未跳过。
- 后端补充精确验证：`cd backend && TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -mod=readonly -race -count=1 -json ./...`；主代理独立解析最终 JSON：13 包、421 顶层/940 含子测试，store 46 含子测试，0 失败、0 跳过。`1e-1000` 自定义正费率与真实 NUMERIC `7e-1000`、7 个旧快照分支、协议能力/影响只读、UTC/非法审计过滤和匿名密码能力均通过。
- 前端验证：`cd frontend && npm run build && npm run test`，18 文件/301 项通过；代理回归 `npm run test -- src/viteProxy.spec.ts` 2 项通过且实际同源 Base URL 返回模型 JSON。包括免费/精度、生命周期/密码、抽屉、导航/URL/加载和跨账号 me/CSRF 回归。
- 真实浏览器/HTTP：桌面侧抽屉独立滚动、390×844 全屏无横向溢出、Escape 回原行/筛选 URL/滚动、正文关闭/刷新清理；一次性明文不留提示条/URL/持久存储。两角色本人接入、真实首次成功、移动 Enter 导航，复制/刷新前后模型计数 33→33。免费正量/version 1，缺价 200 非免费，免积分不报异常；归属/管理员治理拒绝、影响确认/审计实际结果、SSO-only 设置后双登录及其它会话 401。详见 `docs/PRODUCT_ACCEPTANCE_2026-10-03.md`。
- 独立审阅/修复：配置/审计无中高风险；认证/计费发现重复限流桶双计数及二次认证 nil，前端发现迟到 me/CSRF 竞态，均修复、回归并只读复验关闭。主代理实测发现 Vite 缺 `/v1` 代理并修复 dev/preview；末轮精度、历史、协议、审计、密码与代理审阅无中高风险/阻断。真实 HTTP 重复 RPM=10 允许 10 次/第 11 次 429，TPM 只累计一次。
- 中间失败如实保留：两次门禁曾因新增文件未 gofmt 退出；HTTP 脚本首次误用 `ledgers`（接口为 `ledger`）；新增代理测试先遇 jsdom/esbuild 环境冲突，再实际复现两项缺代理断言失败；修复后静态导入 Node config 曾导致类型构建失败，改 Node 环境运行时读取后通过。早期代理失败/中断未当作有效交付，定向旧结果未替代最终门禁。npm allowScripts 与 jsdom scrollTo 为非阻断提示，真实滚动另验。
- 清理：代理浏览器空间关闭；后端、Vite、假模型/假 OIDC 与专用 PostgreSQL 均停止，60963–60967 无监听。只停止本轮 PID/数据库目录，未删除历史数据或测试库目录；脚本、日志、fixture 凭据仅在会话 scratch，文档无凭据/正文。
- 遗留与边界：无本轮阻断或未达标项；F06 按批准范围暂缓。未执行 PostgreSQL 16、远程或生产验收，不把 CI 矩阵当运行证据；提交/推送/部署仍需另行许可。完整 13 条证据见验收文档，供后续会话直接接续。

## 2026-10-02 — 完成逐项需求确认，用户名替代头像，整合计划待许可

- 用户决定原文：“基本都可以，头像改成用户名”。F13 已确认任务化导航、深链/上下文恢复和统一反馈；头像改为会话中的用户名入口，保留账号中心、我的密钥及退出菜单，不用昵称或登录表单变量代替用户名。
- 状态：F01–F13 均已有处理结论（F06 随预算移除暂缓）；本轮未开始实现。用户组运行期撤除的授权扩大影响、模型组保留用途及整体实施许可仍待明确确认。
- 代码基线：`6ccfb89`，分支 `feat/admin-oidc-config`。`git status --short`、`git branch --show-current`、`git rev-parse --short HEAD`、`git diff --stat` 与既有免费计量变更一致；没有回滚或继续修改应用代码。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 和本日志，写入 F13 决定、前后端依赖、验收矩阵、隔离测试/留痕门槛、共享文件单一写入者分工及待确认的撤除兼容建议；未提交、推送、部署或写生产配置。
- 只读核验：后端 explorer 提供全部协议授权入口及模型组统计筛选路径；其长报告被截断，精简回收与前端 explorer 均未交回有效报告，未据此宣称完整核验。主代理直接读取 `backend/internal/store/{groups.go,queries.go}`、`httpapi/api_key.go`、`policy/policy.go`、`frontend/{package.json,src/App.vue,src/api.ts,src/OperationsPane.vue}` 与 `scripts/test.sh`，补齐关键结论。
- 核验结果：组授权与密钥白名单取交集；若撤除组授权，空白名单密钥会按既有白名单语义允许全部启用模型，须用户明确同意。模型组已有非授权统计/请求筛选用途；预算准入与限流计量可分别处理。头像位于 `App.vue:814`，当前取登录表单变量；F08 错误 code/request_id 与 F07/F12 能力契约需前后端同步补齐。
- 测试边界：本轮未运行测试、未访问浏览器或生产。最终验证采用 `make test TEST_DATABASE_URL=...` 与本地假上游/专用测试账号；缺库必须记录集成测试跳过，不复用历史绿灯。
- 下一步：确认用户组完全退出运行授权、保留模型组分类/统计筛选及具体权限变化，再取得整体本地实施许可；随后按独立文件范围启动实现 subagent，主代理集成、独立审阅与测试。提交、推送和部署仍另行确认。

## 2026-10-02 — 确认 SSO 设置密码后双登录，进入导航与反馈需求

- 状态：F01–F05、F07–F12 方向已确认，F06 暂缓；F13 待确认。全部需求确认前不开始实现或启动实现 subagent。
- 用户决定：对 SSO-only 用户设置密码成功后允许 SSO＋本地密码双登录回复“允许”；保留原 OIDC 绑定，设置前禁用找回、登录后免原密码设置的规则不变。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F12 确认范围、F13 待确认建议及本日志；未修改应用代码、生产配置、提交或部署，已有免费计量变更保持原样。
- 核验：仅核对计划及 `docs/PRODUCT_REVIEW_2026-10-02.md` 的 B07/F13 既有证据；未重跑浏览器或测试，不冒称生产再次验证或代码已实现。
- 下一步：确认任务化导航、页面深链/上下文恢复、统一加载/空/错误/成功反馈和头像动作；不恢复用户组/预算、不扩张权限。之后汇总运行期授权兼容、模型组用途、测试验收及 subagent 分工，再获取整体实施许可。

## 2026-10-02 — 调整 SSO-only 密码规则，登录能力边界待确认

- 用户决定原文：“sso-only用户不允许使用找回密码功能，如果是登录后修改密码，则不需要输入原始密码”。F12 已记录该方向，撤回此前隐藏改密表单、拒绝本地改密的建议。
- 确认范围：SSO-only 禁止找回密码；本人登录后可免原密码设置/修改网关密码，仍保留有效会话、CSRF、密码强度及其他会话注销边界；不修改企业身份源密码，不新增完整找回系统或账号重构。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 和本日志；未修改应用代码、启动实现 subagent、生产配置、提交或部署。原有未提交免费计量代码保持不变。
- 核验：定向只读查询 `backend/internal/store/users.go:87-89`，现有 `SetUserPassword` 同时开启 `password_login_enabled`。未运行测试或浏览器，无生产写操作。
- 尚待确认：设置密码后是否允许 SSO＋本地密码双登录；不得因复用现有保存逻辑而静默开启。本条先按 SSO-only 范围记录，不扩大为取消已有本地密码账号的原密码验证。此边界确认后继续 F13，全部需求确认前仍不开始实现。

## 2026-10-02 — 确认业务化价格表单，进入 SSO 密码体验需求

- 状态：F01–F05、F07–F11 方向已确认，F06 暂缓；F12 待确认，F13 尚未讨论。全部需求确认前不开始实现或启动实现 subagent。
- 用户决定：对关联选择、带单位价格表、本地双账试算及高级 JSON 方案回复“可以”；Token 费率准确换算、双账独立、匹配顺序与不可变价格版本边界保留。
- 代码基线：`6ccfb89`，分支 `feat/admin-oidc-config`；本轮 `git status --short`、`git branch --show-current`、`git rev-parse --short HEAD` 与记录一致，已有免费计量变更原样保留。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F11 确认范围、F12 待确认建议及本日志；未修改应用代码、生产配置、提交或部署。
- 核验：定向只读核对 `backend/internal/store/oidc.go:72-89`、`backend/internal/httpapi/self.go:369-427` 及 `frontend/src/App.vue` 的改密表单；SSO 自动创建账号禁用密码登录、使用随机占位密码，现有改密仍要求当前密码。未运行测试或浏览器，不宣称已有实现测试已复验。
- 下一步：确认按账号能力显示密码管理、SSO-only 明确身份源提示和服务端拒绝；本轮不新增本地密码初始化、找回或账号重构。之后继续 F13，再汇总运行期兼容与整体实施门槛。

## 2026-10-02 — 确认密钥生命周期与管理员治理边界

- 状态：F01–F05、F07–F10 方向已确认，F06 暂缓；F11 待确认。全部需求确认前不开始实现或启动实现 subagent。
- 用户决定：同意生命周期方案，并限定管理员对他人密钥仅查看安全信息和停用；本人可维护密钥，管理员不代签、不代续期/改范围、重启或删除他人密钥。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F10 确认范围、F11 待确认建议及本日志；未修改应用代码、生产配置、提交或部署。
- 核验：定向只读核对 `backend/internal/billing/billing.go:184-204`，实际价格匹配为上游模型名优先、别名回退；表单展示不能误承诺别名覆盖。未运行测试或浏览器。
- 下一步：确认带单位价格表、准确十进制转换、独立双账试算、关联选择及高级 JSON；不恢复预算/代签字段，不追溯改历史账本。

## 2026-10-02 — 确认影响面与审计，进入密钥生命周期需求

- 状态：F01–F05、F07–F09 方向已确认，F06 暂缓；F10 待确认。全部需求确认前不开始实现或启动实现 subagent。
- 用户决定：同意影响清单、高风险确认、安全变更摘要和审计筛选分页；不记录凭据/正文，不新增审批或回滚。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F09 确认范围、F10 待确认建议及本日志；未修改应用代码、生产配置、提交或部署。
- 验证边界：只核对已有计划，未重跑测试或浏览器，无生产写操作。
- 下一步：确认本人密钥一次性保存、编辑、有效状态和迁移引导；另明确管理员对他人密钥是否仅查看安全信息与停用，不能把禁止代签误解成已批准所有其他权限变更。

## 2026-10-02 — 确认诊断检索，进入变更影响与审计需求

- 状态：F01–F05、F07–F08 方向已确认，F06 暂缓；F09 待确认。全部需求确认前不开始实现或启动实现 subagent。
- 用户决定：同意安全失败原因、可复制请求 ID、个人记录分页与管理员精确筛选；免费与缺价/结算失败区分，保留权限和敏感信息边界。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F08 确认范围、F09 待确认建议及本日志；没有修改应用代码、生产配置、提交或部署。
- 验证边界：仅核对已有计划与评估记录，未重跑测试或浏览器，无生产写操作。
- 下一步：确认操作前影响清单与高风险确认、操作后安全的前后摘要和结果、审计筛选分页；不建设审批/回滚，不记录凭据或正文。

## 2026-10-02 — 确认快速接入，继续讨论诊断与检索

- 状态：F01–F05、F07 方向已确认，F06 随预算移除暂缓；F08 待确认。全部需求确认前不开始实现或启动实现 subagent。
- 用户决定：同意快速接入区、本人密钥有效模型与安全调用示例；保留本人签发约束，不二次回显明文，不自动发起模型请求。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F07 确认范围、F08 待确认建议与本日志；未修改应用代码、生产配置、提交或部署。
- 验证边界：本轮只核对已有计划和前轮评估结论，未运行测试或重跑浏览器，无生产写操作。
- 下一步：确认用户安全失败原因/请求 ID、管理员多维筛选与结构化错误信息；免费与缺价/结算失败分开，审计检索留 F09 一并确认。

## 2026-10-02 — 确认请求详情交互，进入快速接入需求

- 状态：F01–F05 方向已确认，F06 随预算移除暂缓；F07 待确认。全部需求确认前不开始实现或启动实现 subagent。
- 用户决定：同意请求详情抽屉、紧凑列表与上下文保留；保留既有权限和内容日志确认/审计边界，不新增默认正文展示。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F05 确认范围、F07 待确认建议及本日志；未改应用代码、提交或部署。
- 验证边界：本轮仅核对已有计划，未重跑浏览器或测试，无生产写操作，不宣称实现或验证完成。
- 下一步：确认接入地址、本人密钥有效模型与安全调用示例；不建设用户/组大重构或预算流程，不恢复管理员代签，不自动发起模型请求。

## 2026-10-02 — 确认模型就绪最小方案，继续讨论请求详情

- 状态：F01–F04 方向已确认，F06 随预算移除暂缓；继续逐项确认 F05。全部需求确认前不开始实现。
- 用户决定：同意模型状态汇总、供应商模型关联选择、无有效路由禁止启用的最小方案；保留手工上游模型、免费配置及既有服务兼容边界，不做大型上线流程重构。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F04 确认范围与 F05 待确认方案，以及本日志；没有修改应用代码、启动实现 subagent、生产写操作、提交或部署。
- 验证边界：本轮复核前轮计划与评估记录，未重跑浏览器走查或测试。F05 的详情约 5700px 在前轮已实测，不冒称本轮新验证。
- 下一步：确认请求详情抽屉、紧凑列表和上下文保留；F08 再讨论诊断字段与检索，F13 再讨论页面深链。

## 2026-10-02 — 确认密钥固定归属、管理员不得代签

- 状态：F01–F03 方向已确认，F06 随预算移除暂缓；继续逐项确认 F04。全部需求确认前不实现，不启动实现 subagent。
- 用户决定原文：“是的，而且admin不能给其他用户创建密钥”。所有角色只可为本人签发，归属来自登录会话；创建后不得转移，不能仅在界面限制。
- 范围边界：不自动移除管理员对既有密钥的其他管理能力，其展示/范围留 F10 对齐；不移动历史记录或改变密钥归属。
- 本轮改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 与本日志；F04 记录最小就绪汇总/校验建议，仍待确认。没有修改应用代码、生产写操作、提交或部署。
- 核验：定向只读查询 `backend/internal/httpapi/admin.go`，当前管理端创建仍使用请求中的用户 ID，需在最终实施时做服务端限制。本轮未运行测试。
- 下一步：确认模型就绪检查的汇总展示、路由选择/校验及无有效路由能否启用；免费配置与未定价分开，不自动引入缺价拦截。

## 2026-10-02 — 确认预算先移除，密钥归属另待确认

- 状态：需求逐项确认中，禁止全部确认前开始实现；没有启动实现 subagent、提交或部署。
- 用户决定原文：“预算和上一条一样，先去掉”。预算移除方向已确认，F06 的预算已用/剩余及提额改进随之暂缓；不将此决定扩大为移除 API 密钥或限流。
- 计划范围：移除预算入口、管理/自助展示及预算准入依赖；保留用量、成本/积分统计、历史账本及已有预算数据，不进行破坏性清理。具体运行期兼容措施在最终实施计划中列清。
- 改动文件：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 和本日志；没有修改应用代码或生产配置。本轮未运行测试。
- 尚未确认：F03 的 API 密钥固定归属方案不因预算移除而自动获批，继续单独确认；之后讨论 F04 模型就绪检查。

## 2026-10-02 — 确认移除用户组，继续讨论归属编辑

- 状态：需求确认中，2/13 项方向已确认；全部需求确认前不开始实现。
- 用户决定：本轮先去掉用户组功能；用户和用户组的大重构留待后续。本轮不建设新的基础组/待授权机制，不扩大为删除用户账号或清理历史分组数据。
- 改动：仅更新 `docs/PRODUCT_CHANGE_PLAN.md` 的 F02 决定、撤除兼容边界及 F03 待确认方案；本日志新增本条。没有修改应用代码、启动实现 subagent、提交或部署。
- 核验：只读核对 `backend/internal/store/apikeys.go:70-78`；现有归属更新 SQL 未写入用户 ID，与前轮审查一致。本轮未运行测试，无生产写操作。
- 下一步：确认预算/密钥归属是否固定不可修改（推荐最小修复），再按 F04–F13 继续。旧用户组运行期依赖与权限变化应在最终总计划中明确，不能隐藏入口后留下不可管理的限制或静默扩权。

## 2026-10-02 — 产品修改需求逐项确认，暂停后续实现

- 状态：需求确认中，1/13 项方向已确认；全部确认前禁止继续实现，未提交或部署。
- 代码基线：`6ccfb89`，分支 `feat/admin-oidc-config`；工作区已有免费计量相关未提交实现，本轮原样保留，不擅自回滚，不继续修改。
- 用户决定：免费调用仍正常记录用量；全部需求确认完后才使用 subagent 实施。不得把单项确认当作立即开工许可。
- 本轮改动：新增 `docs/PRODUCT_CHANGE_PLAN.md`，记录 F01 确认方向、F02 待选择的授权策略、其余未确认事项与执行门槛；本日志追加本条。未改应用代码。
- 核验：`git status --short`、`git branch --show-current`、`git rev-parse --short HEAD`、`git diff --stat` 确认已有变更；只读核对 `backend/internal/store/groups.go:258-277`，无组开放与移除最后组恢复旧策略成立。本轮未运行测试，不把上一条测试结果作为本轮验证。
- 下一步：等待用户确认 F02 的新用户默认权限与撤权规则，按 F03–F13 继续讨论；全部确认后核对已有免费计量实现是否符合最终计划，再组织实现、复核和测试。

## 2026-10-02 — 显式零费率：免费调用仍完整记录用量

- 状态：已完成实现、独立审阅与本地验证；未提交、未推送、未部署。仅实施用户批准的免费计量项，其余产品审查建议未实施。
- 清理：本轮创建的临时 PostgreSQL、网关、假上游和 Vite 服务均已关闭，确认本地测试端口无监听；生产站点未变更。
- 代码基线：`6ccfb89`，分支 `feat/admin-oidc-config`。保留接手前 README 的交接段及已有未跟踪 `AGENTS.md`、产品评估和本日志；不覆盖历史记录。批准快照为 `.pi/plan/支持免费调用并完整记录用量-20261002-2222.md`，本条包含可跨会话接续的关键业务约定。
- 实现：
  - `backend/internal/httpapi/admin.go`：积分费率允许显式 `0`，保留高精度；两表必须非空、积分覆盖成本项，负数/非法/漏项/仅旧 `sale` 仍拒绝。
  - `backend/internal/billing/billing.go`：区分积分费率键缺失与显式零；免积分即使有成本也不误报 `ZeroCreditMeters`，缺价及正费率量化为零等既有诊断保留。沿用现有正用量零金额账本与重试幂等路径，无数据库迁移。
  - `backend/internal/gateway/records.go`：仅更新用量独立于定价的注释，不改变调用链。双零不增加金额/积分预算消耗，单账非零仍累计该账；鉴权、授权、RPM/TPM/并发和零上限/耗尽预算的准入规则均未放宽。
  - `frontend/src/App.vue`：新增「设为免费：成本和积分均为 0」，保留 input/output 和两表全部自定义计量项，仍需显式保存；非法 JSON 不部分改写。列表只依据完整费率配置标记免费或免积分。用十进制符号/尾数判断零，避免极小非零费率下溢；手工 JSON 费率要求字符串，防止数字字面量舍入或与 API 的 `map[string]string` 不匹配。
  - `frontend/src/OperationsPane.vue`：缺价提示指引有意免费者创建零费率版本，不将缺价或历史零金额推断为免费；请求详情沿用完整账本展示。
- 测试文件：扩展 `backend/internal/httpapi/admin_resources_test.go`、`backend/internal/billing/{billing_test.go,reliability_test.go}`、`backend/internal/gateway/usage_test.go`、`backend/internal/policy/policy_test.go`；新增 `backend/internal/store/free_usage_test.go`。新增 `frontend/src/AppPrices.spec.ts`，扩展 `OperationsPane.spec.ts`、`DashboardPane.spec.ts`、`MemberConsole.spec.ts`。
- 文档：README 的积分约定说明显式免费仍计量、独立双账及治理边界；`docs/PRODUCT_REVIEW_2026-10-02.md` 的 F01 追加本次确认范围，保留历史线上缺价证据；本日志留痕。
- 可复现测试命令与真实结果（`TEST_DATABASE_URL` 指向本会话 scratch 新建、仅回环可访问的 PostgreSQL 14.24；不是生产库，各 store 用例使用独立临时 schema）：
  ```sh
  cd backend
  go test -mod=readonly -race -count=1 -json ./internal/httpapi ./internal/billing ./internal/gateway ./internal/policy ./internal/store
  cd .. # 回到仓库根目录
  make test TEST_DATABASE_URL="$TEST_DATABASE_URL"
  # 前端定向回归
  cd frontend && npm run test -- src/AppPrices.spec.ts
  ```
  - 后端定向：5 包、447 个测试/子测试通过事件，0 fail、0 skip；新增存储测试的 8 个子场景均实际访问 PostgreSQL 并通过。
  - 完整门禁：退出码 0，gofmt、vet、build、13 个 Go 包的 race 测试通过；`npm ci`、`vue-tsc`/Vite 构建通过；Vitest 9 文件、115 项通过，无数据库跳过。npm 有安装脚本授权的非阻断提示，未更改依赖授权配置。
  - 前端最终价格定向：16/16 通过。新增 API 数字格式用例先红（2 失败、14 通过）后绿；后端零积分创建/结算诊断也分别先复现失败后修复。未为原本正确的预算算法或持久化查询做无必要改动。
  - 本地真实 HTTP + 假上游联调：最终构建的 11 项检查通过，覆盖创建/读回价格、双零/单账收费/正常付费、流式/非流式、估算用量、请求/尝试/版本/零金额账本、统计 Token 不重复、缺价不伪造账本、错误配置拒绝、RPM/TPM 拦截及成员用量。临时脚本仅在 scratch，不作为仓库唯一回归依据；对应稳定回归已纳入上述测试文件。
  - 本地浏览器：自定义 `image`/`cache_read` 保留并在两表置零；点击快捷按钮不保存，非法积分 JSON 不改写成本表；显式保存后读回零费率并显示免费。无生产浏览器写操作。
- 独立审阅：后端/前端 fixer 分工实现；code-reviewer 的 Standards 与 Spec 两路审阅发现旧注释和 JSON 数字费率问题，均已修复并复核关闭；未发现剩余阻断项。可选覆盖建议是再单列未加引号的负指数数值，现有类型拒绝逻辑已覆盖该路径。
- 验证证据：本会话 scratch 的 `backend-targeted-final.jsonl`、`backend-store-free-regression.log`、`free-usage-full-gate.log`、`frontend-rate-format-{red,green}.log`、`free-usage-e2e-final.log`。日志不包含真实凭据或用户请求正文。
- 未完成 / 范围外：本次不补历史账、不改既有价格、不配置线上免费版本、不发布部署。联调另发现响应 `x-request-id` 与持久化请求 ID 不一致，免费请求/尝试/账本已正确关联同一个存储 ID；该既有请求关联链路未在本计划修改，后续可单独排查。CI 的 PostgreSQL 16 矩阵未在本机重跑，本次实跑版本为 14.24。

## 2026-10-02 — 线上管理台产品评估（只读走查与闭环分析）

- 状态：评估已完成，文档已落盘、未提交；未改应用代码或线上配置。
- 代码基线：`6ccfb89`（分支 `feat/admin-oidc-config`，与既有记录一致）。接手时 `AGENTS.md` 和本日志均为已有未跟踪文件，不将它们视为本次新建；期间其他会话更新的 401 排查记录原样保留。
- 摘要：使用用户现有的 `https://omni.zjic.org/` 管理员标签页，查看全部 13 个入口、主要表单和请求/路由/模型详情；前端 explorer、业务规则 explorer 与独立 code-reviewer 分别核验角色流程、依赖规则和高影响断言。
- 改动文件：新增 `docs/PRODUCT_REVIEW_2026-10-02.md`（13 项问题、证据类型、优先级、四条闭环与验收标准）；本日志新增本条。未创建或修改其他交付文件。
- 实测结论：价格页无记录；成功请求详情存在缺价未计费且账本为空；请求列表详情被追加至当前视口外的页面底部。默认开放分组语义、预算/密钥归属 PATCH 不落库等以源码证据单独标注，不冒称生产写操作复现。
- 验证与真实结果：用 `browser-use` 的 AX/DOM 观察复查加载完成后的状态；可复现路径为「仪表盘 → 价格 → 请求记录 → 请求明细」与「请求列表第 1 页点击第 2 条明细」。`git status --short`、`git branch --show-current`、`git rev-parse --short HEAD` 已核对；报告含 13 项，文档无行尾空白。未运行 `make test`/构建/Vitest/数据库测试，因为本次仅分析文档，不声明测试全绿。
- 安全边界：未保存表单、创建密钥、调用付费模型、验证/发现上游、停用/删除对象或读取请求正文。标签页已恢复「用户」页、滚动位置 0，表单已关闭；不记录凭据或用户身份明细。
- 未完成 / 风险：线上构建版本未核对；普通用户/SSO-only 流程为源码核验。需在隔离测试环境复现归属编辑并补测试，明确默认授权、缺价发布策略、预算硬/软上限及历史补账规则。实现与部署均未进行；评估文档未提交，当前只在本机落盘。

## 2026-10-02 — PI-Desktop 的 API key 401 排查与跨会话记录入口

- 状态：当前配置验证正常；历史 401 的具体密钥失效原因未确证。本次文档已落盘，尚未提交。
- 代码基线：`6ccfb89`（排查时分支 `feat/admin-oidc-config`）；未修改网关运行时代码、客户端配置或既有密钥。
- 现场证据（时间均为 UTC）：
  - 桌面端提供商 `OpenAI` 使用 `https://omni.zjic.org/v1`，协议为 OpenAI Chat Completions。
  - `2026-10-02T11:08:29Z` 的桌面端调用日志为 `401 invalid_api_key` / `authentication_error`；模型发现日志中的 401 持续至 `11:39:27Z`。
  - 当前密钥在网关于 `11:39:52Z` 创建，桌面端凭据于 `11:40:22Z` 保存。只读检查确认其哈希匹配数据库中的启用、未设置过期时间的密钥；没有外层空白。凭据只在内存中加载和比对，未输出或写入本日志。
  - 使用该已保存密钥访问域名和直连服务的 `GET /v1/models` 均为 **200**；域名的 `POST /v1/chat/completions` 为 **200**；通过 PI-Desktop 真实提供商调用的测试子任务成功返回 **READY**。
  - 临时新签发密钥的「登录 → 签发 → Bearer 鉴权」链路也通过；测试密钥已删除、测试会话已退出。无管理员密码重置、既有密钥更新或部署。
- 判断边界：旧日志证明当时密钥未通过网关校验，当前保存的密钥及真实桌面调用正常；旧密钥究竟是过期、删除、停用还是误填，现有日志无法区分。没有证据支持修改鉴权代码，也未把别的过期密钥认定为用户使用的密钥。
- 入口：`backend/internal/httpapi/api_key.go` 是数据面鉴权入口；签发在 `self.go` / `admin.go`，哈希在 `backend/internal/crypto/crypto.go`，查库在 `backend/internal/store/apikeys.go`。管理会话 Cookie 与 `/v1` 的 Bearer API key 是两套认证。
- 协议边界：`x-api-key` 仅在 `/v1/messages` 支持；`/v1/models` 要用 `Authorization: Bearer`。桌面端当前配置使用 OpenAI 协议，不能把单独测试 `x-api-key` 访问 models 的 401 当成此次 `invalid_api_key` 的根因。
- 本次改动：新增根目录 `AGENTS.md` 和本日志，补记 OIDC 历史交付；README 增加交接入口。以后每个功能/修复结束时更新本日志的基线、测试证据和遗留项。
- 测试证据：
  ```sh
  cd backend && go test -mod=readonly -count=1 ./internal/httpapi \
    -run '^(TestAPIKeyMiddleware.*|TestAnthropicAPIKeyHeaderFallback|TestSelfCreatesItsOwnAPIKey|TestAdminAPIEncryptsProviderCredentialsAndReturnsAPIKeyOnce)$'
  ```
  结果：`ok github.com/zjvtit/omni-gateway/internal/httpapi 0.952s`。现场 HTTP 及桌面端调用结果如上；临时探针不作为仓库依赖。不涉及业务代码变更，未执行完整 PostgreSQL 集成、race、前端构建/Vitest 门禁，不能记作全量测试通过。
- 重测入口：在已安全加载 `GATEWAY_API_KEY`、`GATEWAY_BASE_URL`（例如 `https://omni.zjic.org/v1`）的环境中，运行 `curl -sS -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer ${GATEWAY_API_KEY}" "${GATEWAY_BASE_URL%/}/models"`，有效密钥应为 200。反馈错误时提供时间、`error.code`、`request_id`，不提供密钥。
- 未完成 / 风险：旧凭据的具体失效原因未确证；若再次出现，先按时间和 request_id 对照最新请求，而非沿用旧日志。文档尚待纳入 Git 提交，未自动提交或推送。

## 2026-09-27 — 主 OIDC Provider 改为管理员拥有的配置（历史补记）

- 状态：功能代码已完成并提交；本条是本次新增的历史交接记录。
- 代码基线：`6ccfb89`；提交范围 `df22e8d`..`6ccfb89`。
- 变更：
  - 网关只保留一个主 OIDC Provider，由管理员在控制台「设置 → 主 OIDC Provider」维护，持久化为 `oidc_provider` 单行表（`backend/internal/store/migrations/0009_oidc_provider.sql`）；`client_secret` 用网关主密钥加密存储、不回显。
  - `GATEWAY_OIDC_*` 只在记录不存在时播种一次；「清空配置」保留禁用记录，因此重启不会把环境变量值复活。此后控制台是唯一事实来源。
  - 运行时经 `backend/internal/httpapi/oidc_provider_source.go` 读取配置并缓存成功结果，写入后失效；管理接口在 `oidc_provider_admin.go`，控制台表单在 `frontend/src/OidcProviderSettings.vue`。
  - fake provider 回调覆盖见 `oidc_callback_test.go`、`oidc_fakeidp_test.go`、`oidc_callback_errors_test.go`；来源、管理、播种覆盖见 `oidc_provider_source_test.go`、`oidc_provider_admin_test.go`、`backend/cmd/server/oidc_provider_seed_test.go`。
  - README 与 `.env.example` 的 OIDC 说明改为「管理员所有」语义（`6ccfb89`）。
- 测试证据：相关提交已加入上述回归测试；本次会话未重跑 OIDC 测试，覆盖存在不等价于当前测试绿灯。
- 未完成：本条不替代当前工作树状态，接手时以 `git status` 和最新日志为准。

## 文档索引（注意各自基线）

- `README.md`：当前配置、API、开发与测试入口。
- `docs/PROJECT_ANALYSIS.md`：架构与阶段记录；页首日期为 2026-08-29，后续已补充 credits 约定，但未覆盖最新 OIDC 配置，不可只按页首日期判断所有内容的版本。
- `docs/ACCEPTANCE_GAPS.md`：初始核对基线 `92b2970`，后续有增量复测；历史 `file:line` 需用当前代码核实，未覆盖 OIDC 配置交付。
- `docs/E2E_TEST_REPORT.md`：基线 `92b2970` + P2/P3，credits 改名前的历史记录。
- `docs/AI_GATEWAY_BENCHMARK.md` / `docs/OPENAI_LIKE_COMPATIBILITY_PLAN.md`：对标/计划记录，需对照后续兼容性提交与 README。
- `docs/LITELLM_ENDPOINT_COMPATIBILITY.md`：endpoint 与 SSE 边界说明。
- `docs/OPTIMIZATION_IMPLEMENTED.md`：优化实施清单。
- `docs/DEPLOY_172.md` / `docs/PODMAN_REMOTE.md`：受限主机 Docker 与远程 Podman 的部署/运维手册。

## 条目模板

```markdown
## YYYY-MM-DD — 一句话标题
- 状态：进行中 / 已完成（已提交 / 未提交）
- 代码基线：`<HEAD 短哈希>`（分支名）
- 改动：按文件/模块列，写行为变化而非代码
- 测试证据：可复现命令 + 真实结果；未运行或跳过的测试写明原因
- 未完成 / 风险：待办、已知缺口、下一步
```
