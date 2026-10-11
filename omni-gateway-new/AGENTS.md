# AGENTS.md — 新会话交接与留痕

本文件只规定接手顺序；能力与配置以 `README.md` 为准，历史与证据见 `docs/DEVELOPMENT_LOG.md`。

## 接手顺序

1. 读 `README.md`，再读 `docs/DEVELOPMENT_LOG.md` 最新 1–3 条，拿到最近记录的基线提交、改动与遗留项。
2. 执行 `git status --short`、`git branch --show-current`、`git rev-parse --short HEAD`，与最新条目的「代码基线」比对；不一致时用 `git log <记录基线>..HEAD --stat` 补差异。
3. 只查与改动子系统相关的文档（见下）；架构/验收类文档多数停在旧基线，不能当作当前状态。
4. 默认不全仓扫描：从入口文件顺调用链定位。

## 按子系统查文档

- 认证 / API：`backend/internal/httpapi/auth.go`、`api_key.go`、`self.go`、`admin.go`；管理端资源列表见 README。
- OIDC：README「主 OIDC Provider」与 `backend/internal/httpapi/oidc.go`、`oidc_provider_source.go`、`oidc_provider_admin.go`、`backend/internal/store/migrations/0009_oidc_provider.sql`。
- 协议兼容：`docs/LITELLM_ENDPOINT_COMPATIBILITY.md`、`docs/OPENAI_LIKE_COMPATIBILITY_PLAN.md`、`docs/AI_GATEWAY_BENCHMARK.md`。
- 部署：`docs/DEPLOY_172.md`、`docs/PODMAN_REMOTE.md`。

## 会话结束必须留痕

在 `docs/DEVELOPMENT_LOG.md` 顶部追加带日期的条目：摘要、改动文件、相关提交（有则写）、可复现的测试命令与真实结果、未完成项。本机新会话可读取已落盘文件；提交进 Git 后记录才会随 clone/pull 传到其他机器。`.pi/` 是已 gitignore 的本机辅助目录，不作为唯一交接来源。禁止写入 API key、密码、`.env` 内容、fixture 凭据或完整用户隐私请求。

## 提交前验证

跑 `make test TEST_DATABASE_URL=...`，它薄封装 `scripts/test.sh`（gofmt、vet、build、`go test -race`、前端构建与 Vitest）。缺 `TEST_DATABASE_URL` 时 store 集成测试自跳过，日志必须如实标注跳过与原因，不得写成全绿。

## 部署红线

部署脚本只允许从干净的 `main` 且与 `origin/main` 一致时人工执行（守卫：`scripts/lib/deploy-guard.sh`）；CI（`.github/workflows/ci.yml`）只跑测试，不自动部署。
