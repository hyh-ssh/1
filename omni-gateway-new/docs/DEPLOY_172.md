# 在 172.20.80.130 上部署（Docker）

网关以 Docker Compose 部署在 `root@172.20.80.130`：

- 容器：`omni-gateway-postgres-1`、`omni-gateway-backend-1`
- 目录：`/opt/omni-gateway`
- 持久化卷：`omni-gateway_postgres_data`
- 对外地址：`http://172.20.80.130:8080`（控制台与 `/v1` 共用该端口）

## 这台主机的特殊之处

部署前做过能力探测，结论是**标准路径都走不通**，这决定了下面的方案：

| 目标 | 结果 |
| --- | --- |
| `docker.io`、`registry-1.docker.io`、`auth.docker.io`、`quay.io`、`github.com`、`deb.debian.org` | DNS 解析失败（本机解析器 `172.20.80.253` 是白名单式） |
| 公网解析器 `223.5.5.5` / `8.8.8.8` / `1.1.1.1` 的 UDP/53 | 出站被封 |
| `docker.nju.edu.cn` | TCP 通，但 manifest 返回 403 |
| `docker.m.daocloud.io` | 401 正常，但鉴权域 `m.daocloud.io` 解析失败；钉扎后变为 `unauthorized` |
| 直连 Docker Hub 兜底 | `EOF` |
| `mirrors.ustc.edu.cn`、`registry.npmmirror.com`、`goproxy.cn` | 可达 |

即 **apt 可用、容器仓库不可用**。因此：

- 镜像**拉不动**：所有基础镜像都由客户端 `save` 后经 SSH 灌入目标机（`docker load`）。
- 多阶段 `Dockerfile` **构建不了**：构建容器连不上 npm registry 与 Go 模块代理。
  所以网关二进制在客户端交叉编译，目标机只用 [`deploy/docker-restricted/Dockerfile.runtime`](../deploy/docker-restricted/Dockerfile.runtime)
  组装运行时层——它与 `Dockerfile` 的 `backend` 阶段逐项对应（同一 `alpine:3.20` 基底、同一
  非 root `app` 用户、同一就绪探针），只把编译步骤换成 `COPY`。

这套"在能联网的机器上编译、在受限主机上组装运行时镜像"的做法与
[`scripts/deploy-podman-remote.sh`](../scripts/deploy-podman-remote.sh) 一致；本机是 Docker
而非 Podman，故另配脚本。

## 部署或更新

```bash
# 客户端需要有 go、npm、openssl、curl，以及一个能访问 Docker Hub 的容器引擎
# （本机用 podman machine；其默认连接可能指向别的主机，所以显式指定）。
CONTAINER_CONNECTION=podman-machine-default \
  scripts/deploy-docker-restricted.sh
```

脚本做的事情：构建 Vue 控制台 → 交叉编译静态 linux 二进制 → 按需把基础镜像灌入目标机 →
上传部署包（`docker-compose.yml`、覆盖文件、`Dockerfile.runtime`、二进制、`.env`）→
在目标机执行 `docker compose up -d --build` → 等待 `/readyz` → 打印容器状态。

它是幂等的：**目标机上已有的 `/opt/omni-gateway/.env` 是权威来源**，会被取回并复用到
`~/.config/llm-gateway/omni-gateway-172.20.80.130.env`（本地副本若不同会先存为 `.bak`），
所以重复执行绝不会再造一套主密钥或数据库密码；PostgreSQL 命名卷也从不被删除。
反之，若目标机没有 `.env` 却仍存在 `omni-gateway_postgres_data` 卷，脚本会**直接报错退出**
——此时新造密钥会让既有数据库失联——需显式设置 `FORCE_NEW_SECRETS=1` 才继续。
可覆盖的变量：`HOST`、`PUBLIC_URL`、`REMOTE_DIR`、`STACK_ENV`、`PLATFORM`、
`CONTAINER_CLIENT`、`CONTAINER_CONNECTION`、`SSH`、`SCP`、`FORCE_NEW_SECRETS`。

### 目标机上的组成

```
/opt/omni-gateway/
├── .env                          # 密钥（0600）
├── docker-compose.yml            # 仓库根目录文件的副本（未改动）
├── docker-compose.override.yml   # 关掉 pull、改用 Dockerfile.runtime
├── Dockerfile.runtime
├── server                        # 交叉编译好的 linux/amd64 二进制
├── ca-certificates.crt           # 取自目标机自身
└── zoneinfo/Asia/Shanghai, UTC   # 取自目标机自身
```

`docker-compose.override.yml` 必须留在**部署目录**里：Compose 会自动合并同名文件，
若放进仓库根目录，日常 `docker compose up` 也会被它影响（那里并没有 `server` 与已加载的镜像）。

CA 证书与时区之所以从目标机拷入，是因为 alpine 不自带这两样、而 `apk` 也连不上自己的 CDN。

## 运维

```bash
ssh root@172.20.80.130
cd /opt/omni-gateway
docker compose ps
docker compose logs -f backend
docker compose restart
curl -fsS http://127.0.0.1:8080/readyz
```

容器使用 `restart: unless-stopped`，`docker` 服务已 `enabled` 开机自启。升级代码只需重新
编译 `server` 并覆盖 `/opt/omni-gateway/server`，再 `docker compose up -d --build`——不需要
任何镜像仓库。

## 密钥处理

同一份密钥存在于两个地方。注意 `~` 指的是**执行脚本的那台客户端机器**（本机为 `/Users/xd`），
不是目标机的 `/root`——目标机上并没有 `~/.config/llm-gateway/` 这个目录：

| 位置 | 作用 |
| --- | --- |
| 客户端 `~/.config/llm-gateway/omni-gateway-172.20.80.130.env`（0600） | 脚本的权威副本；要改配置就改这一份 |
| 目标机 `/opt/omni-gateway/.env`（0600） | 容器实际读取的那一份 |

两者是同一套内容：AES 主密钥、初始管理员密码与 PostgreSQL 密码。在服务器上查初始管理员密码：

```bash
ssh root@172.20.80.130 'grep GATEWAY_ADMIN_PASSWORD /opt/omni-gateway/.env'
```

**请为 AES 主密钥保留受保护的备份**：一旦丢失，已加密的供应商凭据与内容日志将无法恢复。
不要把该文件提交到仓库，也不要在 CI 日志中打印它。客户端副本即使丢失，也能由部署脚本从
目标机取回。

两份文件的优先级很明确：**目标机上正在运行的那份 `.env` 优先**。脚本每次执行都会先把它取回：
本地不存在就直接采用，本地存在但内容不同则先存为
`~/.config/llm-gateway/omni-gateway-172.20.80.130.env.bak`，再采用目标机版本——因为
PostgreSQL 卷的密码与已加密的凭据都只认这一份。上传前会过滤掉 `GATEWAY_DB_URL` 与
`GATEWAY_LISTEN_ADDR`：容器网络内数据库地址由 Compose 从 `POSTGRES_*` 推导，这两个面向
原生部署的变量不应带进容器。

若目标机 `.env` 里的密码与卷内密码不一致（例如误用了新生成的密钥文件），backend 会以
`password authentication failed for user "gateway"` 反复重启；此时应恢复 `POSTGRES_PASSWORD`
与卷匹配的那份密钥文件，而不是删卷重建。

## 已知限制

- **明文 HTTP**：当前 `GATEWAY_COOKIE_SECURE=false`。正式使用前应把 8080 放到做 TLS 终止的
  反向代理之后，并把该项改为 `true`。
- **上游可达性**：该主机的 DNS 是白名单式，因此网关能否访问某个上游供应商域名取决于该域名
  是否在放行列表内；部署后需要按实际供应商验证一次转发。
- **`/etc/hosts` 钉扎**：为绕开间歇性失效的解析器，目标机 `/etc/hosts` 中带有
  `# omni-gateway deploy: pinned` 标记的行把 USTC 镜像、`registry.npmmirror.com`、
  `goproxy.cn`、`docker.m.daocloud.io` 等固定到 IP。若某个镜像站 IP 变动，需要同步更新。
- **旧的原生部署仍留在磁盘上**（systemd 单元 `omni-gateway` 与 `postgresql` 均已
  `disable` + `stop`，二进制在 `/usr/local/bin/omni-gateway`，其环境文件
  `/etc/llm-gateway/gateway.env` 仍指向 `127.0.0.1:5432`，原生库数据在
  `/var/lib/postgresql/17/main`）。回滚方式：先 `docker compose down` 释放 8080，再
  `systemctl enable --now postgresql omni-gateway`。

## 部署门禁：只允许 main

两个部署脚本（[`scripts/deploy-docker-restricted.sh`](../scripts/deploy-docker-restricted.sh)、
[`scripts/deploy-podman-remote.sh`](../scripts/deploy-podman-remote.sh)）都在开始构建之前调用
[`scripts/lib/deploy-guard.sh`](../scripts/lib/deploy-guard.sh) 的 `require_main_branch`，
因此线上只可能跑 main 的代码。它拒绝三种情况：

- **当前 checkout 不是 `main`**（其它分支、detached HEAD 一律拒绝）；
- **工作区不干净**——脚本是在本地树里编译控制台与二进制的，脏树等于把未提交代码带上线；
- **`main` 与 `origin/main` 不一致**（本地领先时先 push）。

逃生阀只有两个，都会在日志里留下警告、绝不静默：`SKIP_BRANCH_GUARD=1` 跳过全部门禁，
`ALLOW_UNPUSHED_MAIN=1` 只放行"本地领先 origin/main"。它们只该用于紧急回滚之类的场景。

因此标准上线流程是：在功能分支提交 → 合并进 main（`git switch main && git merge --ff-only <branch>`）
→ `git push origin main` → 从 main 的干净 checkout 运行部署脚本。

若主工作区还留着别的分支的未提交改动，不要为了让门禁放行而 stash 它们；另开一棵干净的 main 工作树即可：

```bash
git worktree add /tmp/deploy-main main
cd /tmp/deploy-main
CONTAINER_CONNECTION=podman-machine-default scripts/deploy-docker-restricted.sh
cd - && git worktree remove /tmp/deploy-main
```
