# ai-ops-relay

让大模型（或任何 MCP 客户端）安全地“对话”到内网机器：看日志、查状态、执行白名单内的
argv 命令。内网侧只主动外连 443，**不需要开放任何入站端口**。

> **占位符说明**
>
> 本文中的 `relay.example.com`、`192.0.2.10`、`intranet-01`、`<令牌>` 都是示例。
> 请替换成你自己的值，并且不要把真实公网 IP、域名、令牌、私钥提交到 Git。
> 仓库自带 `scripts/check-secrets.sh`，提交前会自动检查这些内容。

## 特性

- **只出站**：内网机器运行 `relay-agent`，主动连接公网 Relay；内网防火墙无需入站规则。
- **MCP 原生**：内置 MCP stdio server，提供 `list_hosts` / `tail_log` / `exec` / `get_task` 四个工具。
- **默认拒绝**：Relay 侧按 argv 白名单判定，命令不经过 shell，杜绝 `;`、`|` 等拼接。
- **SSH 跳板策略**：可按网段、端口、用户限制跳板机，禁止 `-L/-R/-D/-ProxyCommand`。
- **长轮询 + 自动重连**：Agent 断线后指数退避重连；任务异步执行，输出有上限。
- **全量审计**：每条命令、退出码、耗时写入 `audit.jsonl`。
- **跨平台**：Linux / Windows Agent；Linux 可作为 systemd 服务常驻，Windows 支持临时会话和计划任务。
- **443 复用**：同一个端口同时承载 HTTPS 和 SSH（可选），方便只放行 443 的网络。

## 架构

```text
┌──────────────┐  出站 443   ┌───────────────────────────┐
│ 内网机器     │ ──────────► │ Relay（公网 VPS:443）      │
│ relay-agent  │ ◄────────── │ 长轮询 + 白名单 + 审计     │
└──────────────┘             └───────────────────────────┘
                                  ▲              ▲
                                  │ HTTPS        │ HTTPS
                           ┌──────┴─────┐  ┌─────┴──────┐
                           │ MCP 客户端 │  │ 人工控制台  │
                           │ (LLM)      │  │ curl 等    │
                           └────────────┘  └────────────┘
```

- 两端都主动出站，Relay 是唯一交汇点。
- Agent 与 Relay 之间用每个主机独立的令牌认证；MCP/控制面用另一个 API 令牌。
- 任务只携带 argv，不经过 shell；白名单在 Relay 侧统一判定。
- Relay 可在 443 上同时提供 HTTPS 和 SSH（协议嗅探复用，见 `internal/relay/mux.go`）。

## 目录结构

| 路径 | 说明 |
|---|---|
| `cmd/relay` | Relay 服务：`serve` / `gencert` / `mcp` 三个子命令 |
| `cmd/agent` | 内网侧 Agent（Linux / Windows / macOS） |
| `internal/relay` | 长轮询队列、任务状态、审计、443 协议复用 |
| `internal/agent` | 任务执行、超时杀进程树、跨平台环境处理 |
| `internal/policy` | argv 白名单、通配规则、SSH 跳板策略、注入拦截 |
| `internal/mcp` | MCP stdio server |
| `dist/` | 安装脚本与编译产物（二进制被 `.gitignore` 排除） |
| `deploy/` | systemd unit、策略和环境变量示例 |
| `tools/` | 调用 Relay HTTP API 的 Python 小工具 |
| `scripts/check-secrets.sh` | 发布前检查私钥、令牌、公网 IP |

## 环境要求

- 构建：Go 1.22+
- Relay：一台有公网 IP 的 Linux VPS，放行 TCP 443
- Agent：Linux（amd64/arm64）或 Windows（amd64/arm64）；macOS 可从源码构建
- 可选：`jq`、`curl`、`openssl` 用于调试和生成令牌

## 快速开始

### 1. 构建

```bash
git clone https://github.com/runzebra01-cmd/ai-ops-relay.git
cd ai-ops-relay

# 一条命令构建 Linux Relay 和 Linux/Windows Agent
make build
```

构建产物：

```text
dist/relay-linux-amd64
dist/relay-linux-arm64
dist/relay-agent-linux-amd64
dist/relay-agent-linux-arm64
dist/relay-agent-windows-amd64.exe
dist/relay-agent-windows-arm64.exe
```

也可以手工构建单个目标：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags '-s -w' -o dist/relay-linux-amd64 ./cmd/relay

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags '-s -w' -o dist/relay-agent-linux-amd64 ./cmd/agent
```

### 2. 生成令牌

每台机器一个独立令牌，控制面令牌单独一个。全部用随机值，不要手写。

```bash
openssl rand -hex 16   # Relay 控制面 API token
openssl rand -hex 16   # intranet-01 的 Agent token
openssl rand -hex 16   # 其它机器再来一个
```

把结果写进服务器上的环境文件（**不要提交到 Git**）：

```bash
sudo install -d -m 750 -o root -g relay /etc/relay
sudo install -m 600 deploy/relay.env.example /etc/relay/relay.env
sudoedit /etc/relay/relay.env
```

内容示例：

```ini
RELAY_API_TOKEN=<控制面令牌>
RELAY_AGENTS=intranet-01=<主机令牌>,win-01=<另一台主机令牌>
RELAY_DOWNLOAD_TOKEN=<可选，安装包下载令牌>
```

`RELAY_AGENTS` 的格式是 `host_id=token`，用逗号分隔。`host_id` 必须和 Agent 启动时的
`-host` 完全一致。

### 3. 部署 Relay

```bash
# 运行用户与目录
sudo groupadd --system relay 2>/dev/null || true
sudo useradd --system --gid relay --home /var/lib/relay \
  --shell /usr/sbin/nologin relay 2>/dev/null || true
sudo install -d -m 755 /opt/relay/bin
sudo install -d -m 750 -o root  -g relay /etc/relay
sudo install -d -m 750 -o relay -g relay /var/log/relay /var/lib/relay /var/lib/relay/dl

# 二进制
sudo install -m 755 dist/relay-linux-amd64 /usr/local/bin/relay

# 自签 TLS 证书：把 relay.example.com 换成你的域名或公网 IP
sudo /usr/local/bin/relay gencert -host relay.example.com -out /etc/relay
sudo chown root:relay /etc/relay/cert.pem /etc/relay/key.pem
sudo chmod 644 /etc/relay/cert.pem
sudo chmod 640 /etc/relay/key.pem

# 命令白名单
sudo install -m 640 -o root -g relay deploy/policy.example.json /etc/relay/policy.json

# systemd
sudo install -m 644 deploy/relay.service /etc/systemd/system/relay.service
sudo systemctl daemon-reload
sudo systemctl enable --now relay
sudo systemctl status relay --no-pager
```

自签证书时，把公网证书复制给客户端和 Agent：

```bash
# 从 Relay 取回（或直接在服务器上复制）
sudo cp /etc/relay/cert.pem ./ca.pem
```

健康检查：

```bash
# 自签证书
curl --cacert ./ca.pem https://relay.example.com/healthz
# 或临时跳过校验
curl -k https://relay.example.com/healthz
```

预期输出：`{"status":"ok"}`。

防火墙/安全组只需放行 TCP 443（如果 Relay 同时复用 SSH，也只需要 443）。

### 4. 接入内网机器

#### Linux（常驻 systemd）

把以下文件放到内网机器的同一目录：

```text
relay-agent-linux-amd64     # 按架构选择
install.sh
ca.pem                      # 自签证书时必需
```

然后执行：

```bash
sudo bash install.sh \
  -relay https://relay.example.com \
  -host intranet-01 \
  -token '<主机令牌>' \
  -ca ./ca.pem
```

可选参数：

```bash
-proxy http://proxy.corp:3128   # 通过企业 HTTP 代理出网
```

安装脚本会写入 `/etc/relay-agent/agent.env`、注册并启动 `relay-agent.service`。
验证：

```bash
systemctl status relay-agent
journalctl -u relay-agent -n 50 --no-pager
```

看到 `connected to https://... as host "intranet-01"` 即为成功。

#### Linux（临时运行，不常驻）

```bash
export RELAY="https://relay.example.com"
export RELAY_AGENT_TOKEN="<主机令牌>"
./dist/relay-agent-linux-amd64 \
  -relay "$RELAY" \
  -host intranet-01 \
  -token "$RELAY_AGENT_TOKEN" \
  -ca ./ca.pem \
  -labels env=prod,site=bj
```

`-labels` 只是展示用的键值对，MCP 的 `list_hosts` 会返回它们。

#### Windows（临时会话，推荐）

编辑 `dist/start-agent.cmd` 顶部三行：

```bat
set "RELAY=https://relay.example.com"
set "HOSTID=win-01"
set "TOKEN=<主机令牌>"
```

双击运行。窗口开着就能被访问，**关掉窗口立即断开**。权限模型很直观：
窗口以什么身份运行，模型就只有什么权限。

> 注意：`.cmd` 里的令牌是明文。改好的文件只放在本机，不要提交到 Git。

#### Windows（常驻计划任务）

以管理员身份打开 PowerShell：

```powershell
.\dist\install-agent.ps1 `
  -Relay https://relay.example.com `
  -HostId win-01 `
  -Token '<主机令牌>' `
  -Ca .\ca.pem
```

安装到 `C:\ProgramData\RelayAgent` 目录，注册 SYSTEM 权限的开机自启计划任务。
查看日志和停止任务：

```powershell
Get-Content C:\ProgramData\RelayAgent\agent.log -Tail 20
schtasks /End /TN RelayAgent
schtasks /Delete /TN RelayAgent /F
```

详细说明见 [`dist/README.md`](dist/README.md)。

### 5. 确认机器在线

```bash
export RELAY="https://relay.example.com"
export RELAY_API_TOKEN="<控制面令牌>"

curl --cacert ./ca.pem \
  -H "Authorization: Bearer $RELAY_API_TOKEN" \
  "$RELAY/v1/hosts" | jq
```

应能看到 `id` 为 `intranet-01`、`online` 为 `true` 的记录。

### 6. 接入 MCP 客户端

先确认 `relay mcp` 能启动：

```bash
RELAY_API_TOKEN='<控制面令牌>' \
  /usr/local/bin/relay mcp \
  -api https://relay.example.com \
  -ca ./ca.pem
```

然后在 MCP 客户端配置中加入（不同客户端字段名略有差异）：

```json
{
  "mcpServers": {
    "intranet": {
      "command": "/usr/local/bin/relay",
      "args": [
        "mcp",
        "-api", "https://relay.example.com",
        "-ca", "/etc/relay/cert.pem"
      ],
      "env": {
        "RELAY_API_TOKEN": "<控制面令牌>"
      }
    }
  }
}
```

说明：

- `mcp` 子命令不传 `-token` 时会读取环境变量 `RELAY_API_TOKEN`。
- 如果客户端不支持 `env`，可以显式传 `-token`，但**不要把这个配置文件提交到任何仓库**。
- 自签证书时 `-ca` 必须指向 Relay 的 `cert.pem`；生产环境不建议用 `-insecure`。
- MCP 使用 stdin/stdout 传 JSON-RPC，日志会写到 stderr。

## MCP 工具

| 工具 | 用途 | 关键参数 |
|---|---|---|
| `list_hosts` | 列出机器、在线状态、标签、待执行任务数 | 无 |
| `tail_log` | 看文件日志或 `journalctl -u` | `host` + `path` 或 `unit`, `lines` |
| `exec` | 按 argv 执行命令（受白名单约束） | `host`, `argv`, `timeout_ms`, `cwd` |
| `get_task` | 查询异步任务结果 | `task_id` |

示例对话：

- “列出所有内网机器” → `list_hosts`
- “看一下 intranet-01 上 nginx 最近 200 行日志” → `tail_log(host=intranet-01, unit=nginx, lines=200)`
- “在 intranet-01 上执行 df -h” → `exec(host=intranet-01, argv=["df","-h"])`

## 命令白名单

策略文件默认路径 `policy.json`，由 `relay serve -policy` 指定。Relay 启动时加载；
修改后需要重启 Relay。

```json
{
  "mode": "readonly",
  "allow": [
    ["tail"],
    ["journalctl"],
    ["systemctl", "status"],
    ["df"],
    ["docker", "logs"]
  ],
  "deny": [
    ["rm"],
    ["sudo"],
    ["bash"]
  ],
  "hosts": {
    "intranet-01": {
      "mode": "exec",
      "allow": [
        ["systemctl", "restart", "nginx"]
      ]
    }
  }
}
```

规则说明：

- `mode`：
  - `readonly`：只看 `allow` 列表（默认）。
  - `exec`：同 `readonly`，但可按需放行写操作/重启服务。
  - `full`：除 `deny` 外全部放行，仅供调试，不建议生产使用。
- `allow` 中的每一项是 argv 前缀。例如 `["systemctl","status"]` 允许
  `systemctl status nginx`，但不允许 `systemctl stop nginx`。
- 最后一段支持 `*` 前缀通配，例如
  `["powershell.exe","-NoProfile","-NonInteractive","-Command","Get-Content*"]`。
  通配段中出现 `;`、`|`、`&`、`$`、`(`、`)`、`<`、`>`、反引号或换行会被直接拒绝。
- `deny` 优先于 `allow`，且会与内置默认 deny 列表合并。
- `hosts.<id>` 可以覆盖单台机器的 `mode`、追加 `allow`/`deny`，或配置 `ssh` 跳板策略。

### SSH 跳板策略

让某台跳板机可以 `ssh` 到指定网段，而不是成为任意 TCP 代理：

```json
{
  "hosts": {
    "jump-01": {
      "mode": "exec",
      "ssh": {
        "key": "/var/lib/relay-agent/relay_jump",
        "cidrs": ["10.20.30.0/24"],
        "ports": [22],
        "users": ["ops"]
      }
    }
  }
}
```

限制：

- `-i` 私钥路径必须与 `ssh.key` 完全一致。
- 目标必须是 IP 字面量，且在 `cidrs` 内；不接受主机名（防 DNS 重绑定）。
- 端口必须在 `ports` 内；用户必须在 `users` 内（留空表示不限制用户）。
- `-L`、`-R`、`-D`、`-W`、`-J`、`-F`、`ProxyCommand` 等一律拒绝。
- `unrestricted: true` 会跳过以上目标校验，只建议在完全可控的环境使用。

## HTTP API

Relay 同时是 Agent 和控制面的 API 服务。Agent 使用主机令牌，控制面使用 API 令牌。

| 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|
| GET | `/healthz` | 无 | 健康检查 |
| POST | `/v1/agent/handshake` | Agent token | Agent 注册 |
| GET | `/v1/agent/poll` | Agent token | 长轮询取任务 |
| POST | `/v1/agent/result` | Agent token | 回传结果 |
| GET | `/v1/hosts` | API token | 机器列表 |
| POST | `/v1/tasks` | API token | 创建任务 |
| GET | `/v1/tasks/{id}` | API token | 查询任务 |
| GET | `/dl/{name}?k=<token>` | 下载 token | 下载安装包 |

创建任务示例：

```bash
curl --cacert ./ca.pem \
  -H "Authorization: Bearer $RELAY_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"host_id":"intranet-01","args":["df","-h"],"timeout_ms":10000}' \
  https://relay.example.com/v1/tasks
```

`POST /v1/tasks` 返回 `202` 和 `task.id`，随后轮询：

```bash
curl --cacert ./ca.pem \
  -H "Authorization: Bearer $RELAY_API_TOKEN" \
  https://relay.example.com/v1/tasks/<task-id>
```

`timeout_ms` 默认 30000，最大 600000。相同 `idem` 值的任务会复用已有任务。

## Relay CLI

```bash
relay serve [flags]
relay gencert -host <域名或IP> -out <目录> [-days 825]
relay mcp -api <URL> [-token <token>] [-ca <ca.pem>] [-insecure]
```

`relay serve` 常用参数：

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-addr` | `:8443` | 监听地址，生产通常是 `0.0.0.0:443` |
| `-api-token` | `$RELAY_API_TOKEN` | 控制面 API 令牌 |
| `-agents` | 无 | `host=token,host2=token2` |
| `-policy` | `policy.json` | 白名单文件；不存在时用内置只读策略 |
| `-audit` | `audit.jsonl` | 审计输出；留空则不写 |
| `-tls-cert` / `-tls-key` | 空 | TLS 证书和私钥；为空则明文 HTTP |
| `-ssh-proxy` | 空 | 非 TLS 连接转给本地 sshd，例如 `127.0.0.1:22` |
| `-download-dir` | 空 | 静态下载目录 |
| `-download-token` | `$RELAY_DOWNLOAD_TOKEN` | 下载令牌 |
| `-poll` | `25` | 长轮询挂起秒数 |
| `-max-out` | `262144` | 单个输出流最大字节数 |

`relay mcp` 常用参数：

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-api` | 无 | Relay 地址，必填 |
| `-token` | `$RELAY_API_TOKEN` | 控制面令牌 |
| `-ca` | 空 | Relay 自签证书路径 |
| `-insecure` | `false` | 跳过 TLS 校验，仅测试 |

## Agent CLI

```bash
relay-agent -relay <URL> -host <host_id> -token <token> [flags]
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-relay` | 无 | Relay 地址，必填 |
| `-host` | 无 | 本机在 Relay 中的唯一主机名 |
| `-token` | `$RELAY_AGENT_TOKEN` | 主机令牌 |
| `-labels` | 空 | 标签，例如 `env=prod,site=bj` |
| `-ca` | 空 | Relay 证书路径（自签时使用） |
| `-insecure` | `false` | 跳过 TLS 校验，仅测试 |
| `-check` | `false` | 只做一次握手验证后退出 |
| `-max-out` | `262144` | 单次输出上限字节数 |
| `-version` | `false` | 打印版本 |

安装后建议先用 `-check` 做连通性检查：

```bash
relay-agent -check -relay https://relay.example.com -host intranet-01 \
  -token "$RELAY_AGENT_TOKEN" -ca /etc/relay-agent/ca.pem
```

## Python 小工具

`tools/rr.py` 是调用 HTTP API 的最小示例，适合排查问题：

```bash
export RELAY="https://relay.example.com"
export RELAY_API_TOKEN="<控制面令牌>"
export RELAY_CA="./ca.pem"          # 自签证书时设置

python3 tools/rr.py intranet-01 '["tail","-n","100","/var/log/app.log"]'
```

其它脚本：

- `tools/winexec.py`：在 Windows 主机上执行命令，并补齐 OpenSSH 常用环境变量。
- `tools/lnx.py`：通过 Relay 跳板机执行 SSH 命令（需要在策略中配置 `ssh`）。
- `tools/relay_client.py`：三个脚本共用的 HTTP 客户端。

它们只从环境变量读取地址和令牌，不在代码里保存任何真实值。

## 安全与隐私

这是使用本项目时最重要的部分。

### 不要提交的内容

以下内容应始终留在你自己的机器/服务器上：

- 真实公网 IP、域名、内网拓扑
- `RELAY_API_TOKEN`、`RELAY_AGENT_TOKEN_*`、`RELAY_DOWNLOAD_TOKEN`
- TLS 私钥 `key.pem`、SSH 私钥、`known_hosts`
- `.env`、部署脚本中的口令、`audit.jsonl`、Agent/Relay 日志
- 编译产物和安装包（体积大且可能带证书）

仓库的 `.gitignore` 已排除 `.deploy/`、`.test/`、`.toolchain/`、`build/`、`bin/`、
`dist/` 下的二进制、证书、私钥、`.env`、日志等。如果你新增了敏感目录，请同步更新
`.gitignore`。

### 发布前检查

```bash
# 扫描已跟踪 + 未跟踪但未忽略的文件
make secret-check
```

检查项包括：私钥内容、32 位十六进制令牌、明文口令、真实公网 IPv4。
GitHub Actions 也会在每次 push/pull request 时运行同一脚本（见
`.github/workflows/secret-scan.yml`）。如果检查失败，**不要推送**。

### 如果已经泄露

1. 立即在 Relay 上更换所有令牌，并重启 Relay。
2. 更新每台 Agent 的 `agent.env` / `start-agent.cmd`，重启 Agent。
3. 如果私钥泄露，重新生成证书和 SSH 密钥。
4. 如果已经推到 GitHub，删除仓库或清理 Git 历史，并注意 GitHub 的缓存/派生仓库
   可能仍保留旧内容；轮换令牌比清理历史更重要。

### 运行时加固

- Relay 使用独立系统用户 `relay`，`NoNewPrivileges=true`，只写日志和数据目录。
- Agent 能用什么权限，取决于运行它的用户/窗口；建议用专用低权限用户运行。
- 生产环境优先使用受信任 CA 证书；自签证书时通过安全渠道分发 `cert.pem`。
- 公网只放行 443；如无必要，不要暴露 22。
- 白名单遵循最小权限：先 `readonly`，确有必要再按主机追加具体 argv。
- 审计日志 `audit.jsonl` 建议单独备份或转发到日志系统。

## 常见问题

**443 已被 sshd 占用怎么办？**

`relay` 可以在 443 上做协议复用：

```bash
relay serve -addr 0.0.0.0:443 -ssh-proxy 127.0.0.1:22 ...
```

sshd 需要监听另一个端口（例如 22），非 TLS 连接会被转给 `-ssh-proxy`。
如果 443 被 Nginx 等占用，可让 Relay 监听 `127.0.0.1:8443`，由 Nginx 反代 HTTPS。

**Agent 连接失败怎么排查？**

1. 内网机器能否访问 Relay：`curl --cacert ca.pem https://relay.example.com/healthz`
2. 是否需要走代理：给 Agent 加 `-proxy`（安装脚本）或设置 `HTTPS_PROXY`。
3. 令牌是否抄错、`-host` 是否与 Relay 的 `RELAY_AGENTS` 一致。
4. 出口防火墙是否放行 443。
5. 用 `relay-agent -check ...` 单独验证握手。

**自签证书报 `x509: certificate signed by unknown authority`**

给 Agent 加 `-ca /path/ca.pem`，MCP 加 `-ca`，curl 加 `--cacert`。
`-insecure` 只用于临时测试，会失去中间人防护。

**Windows 上为什么不能用管道 `|`？**

命令按 argv 直接执行，不经过 `cmd.exe`/PowerShell 解析，管道会被当成普通参数。
请改用命令自身的参数，例如 `Get-Content -Tail 100`、`Select-String` 等，
或在策略中放行一个包装脚本（需要自行评估风险）。

**任务一直 pending / 主机离线？**

`GET /v1/hosts` 查看 `online` 和 `last_seen`；确认 Agent 进程在运行、能出网、
令牌正确。Relay 重启后内存中的任务会丢失，需要重新下发。

**怎么重置令牌？**

改 Relay 上的 `RELAY_AGENTS` / `RELAY_API_TOKEN`，重启 Relay；再改每台 Agent 的
`agent.env` 或 `start-agent.cmd`，重启 Agent。控制面客户端同步更新。

**支持 macOS 吗？**

可以。Agent 使用 `//go:build unix` 实现，`GOOS=darwin GOARCH=arm64 go build ./cmd/agent`
可构建；安装脚本目前只覆盖 Linux 和 Windows。

## 开发

```bash
make test          # go test ./...
make vet           # go vet ./...
make fmt           # gofmt
make build         # 交叉编译
make secret-check  # 发布前敏感信息检查
```

## 已知限制

- Relay 是单实例，任务状态在内存中；重启会丢失待执行任务，并断开复用的 SSH 会话。
- 审计日志未做防篡改（后续可加哈希链）。
- 控制面 API 令牌是全有或全无，没有细粒度用户权限。
- Windows 不支持 shell 管道；输出超过 `-max-out` 会被截断。
- 自签证书需要手动分发 CA；生产环境建议换成受信任证书。
- `/dl/` 只提供静态文件下载，不支持上传。

## License

尚未添加开源许可证。如果要公开分发，请根据你的需要补充 `LICENSE` 文件。
