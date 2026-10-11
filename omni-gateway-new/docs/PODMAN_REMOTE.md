# 远程 Podman 部署

网关通过 Podman SSH 连接 `hermes-remote` 部署：

- Podman 服务：`root@192.168.2.21`
- 对外地址：`http://192.168.2.21:8080`
- 容器：`llm-gateway-postgres`、`llm-gateway-backend`
- 持久化卷：`llm-gateway_postgres_data`
- 网络：`llm-gateway-net`

## 重新部署或更新

远程主机目前无法稳定拉取 Docker Hub 镜像。请使用远程部署脚本：它在客户端构建
Linux Go 二进制与 Vue 资源，基于远程已缓存的 Ubuntu 基础镜像构建一个小体积
运行时镜像，并使用 Quay 上可用的 PostgreSQL 镜像：

```bash
# 如尚未配置，先安装 Podman SSH 连接。
podman system connection list

# 部署用的密钥文件保存在仓库之外。
ENV_FILE="$HOME/.config/llm-gateway/hermes-remote.env" \
PUBLIC_URL=http://192.168.2.21:8080 \
./scripts/deploy-podman-remote.sh
```

该脚本对指定的网关容器是幂等的：它会重建容器，但保留 PostgreSQL 命名卷，也绝不会
删除 Podman 主机上无关的容器。

客户端需要的工具是 `podman`、`go`、`npm`、`curl`，以及已安装的 zoneinfo 数据库。
Podman 连接必须能访问远程 socket。远程镜像仓库不可用时，远程基础镜像必须已存在于
本地；只有在镜像已存在于该 Podman 主机上时，才可覆盖 `REMOTE_BASE_IMAGE`。

## 运维

```bash
podman --connection hermes-remote ps --filter name=llm-gateway
podman --connection hermes-remote logs -f llm-gateway-backend
podman --connection hermes-remote logs -f llm-gateway-postgres
curl http://192.168.2.21:8080/healthz
curl http://192.168.2.21:8080/readyz
```

容器使用 `--restart always`。远程主机启用了 `podman-restart.service`，因此 Podman
启动时容器会被重新拉起。生产环境请把该地址放到 TLS 反向代理之后，并在下一次部署前
设置 `GATEWAY_COOKIE_SECURE=true`。

## 密钥处理

生成的环境文件位于 `$HOME/.config/llm-gateway/hermes-remote.env`，权限受限。它包含
AES 主密钥、初始管理员密码与 PostgreSQL 密码。请为 AES 主密钥保留一份受保护的
备份：一旦丢失，已加密的供应商凭据与内容日志将无法恢复。不要把该文件提交到仓库，
也不要在 CI 日志中打印它。

当远程主机能够拉取 `postgres:16-alpine`、`node:20-alpine` 与 `golang:1.23-alpine`
时，标准 `docker-compose.yml` 仍然可用。对于当前离线/半联网的远程 Podman 主机，
请改用上面的脚本，而不是把构建交给本地 Docker Compose provider。
