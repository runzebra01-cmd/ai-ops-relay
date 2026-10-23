# 内网机器接入包（relay-agent）

支持 Linux（x86_64 / arm64）和 Windows（x64 / ARM64）。
Agent 只建立**出站** TCP 443 连接，内网不需要开放任何入站端口。

> `relay.example.com`、`intranet-01`、`<令牌>` 都是示例，请换成你自己的值。
> 令牌不要写进脚本后提交到 Git，见仓库根目录 README 的“安全与隐私”一节。

## 目录内容

打包后通常包含：

```
relay-agent-bundle/
├── install.sh                     # Linux 常驻安装
├── install-agent.ps1              # Windows 常驻安装
├── start-agent.cmd                # Windows 临时会话
├── start-ubuntu-jump.sh           # Linux 临时跳板
├── relay-agent-linux-amd64
├── relay-agent-linux-arm64
├── relay-agent-windows-amd64.exe
├── relay-agent-windows-arm64.exe
└── ca.pem                         # Relay 自签证书（由你生成，不提交到仓库）
```

如果使用自签证书，请把 Relay 上 `/etc/relay/cert.pem` 复制为这里的 `ca.pem`。
用公网 CA 证书时可省略 `-ca`。

## Linux —— 常驻服务

```bash
tar xzf relay-agent-bundle.tar.gz -C /tmp
sudo bash /tmp/relay-agent-bundle/install.sh \
  -relay https://relay.example.com \
  -host intranet-01 \
  -token '<主机令牌>'
```

可选参数：

```bash
-proxy http://proxy.corp:3128   # 走企业 HTTP 代理
-ca /path/to/ca.pem             # 指定 Relay 证书
```

脚本会：安装二进制到 `/usr/local/bin/relay-agent` → 写
`/etc/relay-agent/agent.env` → 做一次连通性检查 → 注册并启动
`relay-agent.service`。

验证：

```bash
systemctl status relay-agent
journalctl -u relay-agent -n 50 --no-pager
```

看到 `connected to https://... as host "intranet-01"` 即成功。

## Linux —— 临时跳板（不常驻）

适合只在需要时把某台机器接进来，关掉窗口就下线：

```bash
export RELAY="https://relay.example.com"
export RELAY_AGENT_TOKEN="<主机令牌>"
bash start-ubuntu-jump.sh -host ubuntu-jump -ca /path/to/ca.pem
```

也可以直接用 `-relay` / `-host` / `-token` 传参。`-insecure` 会跳过 TLS
校验，只建议在本地测试时使用。

## Windows —— 临时会话模式（推荐，不常驻）

双击 `start-agent.cmd` 打开一个窗口即可。窗口开着就能被访问，**关掉窗口立即断开**。

首次使用先编辑 `start-agent.cmd` 顶部的三行：

```bat
set "RELAY=https://relay.example.com"
set "HOSTID=win-01"
set "TOKEN=<主机令牌>"
```

权限模型很直观：**窗口以什么身份运行，模型就只有什么权限**；管理员窗口权限大，
普通窗口权限小。注意 `.cmd` 里的令牌是明文，不要把改好的文件提交到 Git。

## Windows —— 常驻服务模式（可选）

以管理员身份打开 PowerShell，进入安装包目录：

```powershell
.\install-agent.ps1 -Relay https://relay.example.com -HostId win-01 -Token '<主机令牌>'
```

常用参数：

```powershell
-Ca C:\path\ca.pem      # 指定 Relay 证书
-Proxy http://proxy.corp:3128
```

脚本会安装到 `C:\ProgramData\RelayAgent` 目录，做连通性检查，然后注册开机自启计划任务
（SYSTEM 权限）。

```powershell
Get-Content C:\ProgramData\RelayAgent\agent.log -Tail 20
schtasks /End /TN RelayAgent
schtasks /Delete /TN RelayAgent /F
```

## 验证

```bash
# Linux
systemctl status relay-agent
journalctl -u relay-agent -n 20

# Windows
Get-Content C:\ProgramData\RelayAgent\agent.log -Tail 20
```

日志出现 `connected to https://... as host "xxx"` 即为成功。

## 命令白名单

命令**不经过 shell**，按 argv 执行，放行规则在 Relay 侧 `policy.json`。
默认策略是只读。Windows 主机建议在 `hosts` 里单独放行，例如：

```json
{
  "hosts": {
    "win-01": {
      "mode": "exec",
      "allow": [
        ["hostname"],
        ["ipconfig"],
        ["netstat"],
        ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-Content*"],
        ["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Get-Process*"]
      ]
    }
  }
}
```

末位 `*` 是前缀通配；通配段里出现 `;`、`|`、`&`、`$`、`(`、`)`、`<`、`>`、反引号等字符会被直接拒绝，
防止 `Get-Content x; Remove-Item ...` 这类拼接。

改完执行 `systemctl restart relay` 生效。

## 卸载

Linux：

```bash
systemctl disable --now relay-agent
rm -f /etc/systemd/system/relay-agent.service /usr/local/bin/relay-agent
rm -rf /etc/relay-agent
systemctl daemon-reload
```

Windows：先 `schtasks /Delete /TN RelayAgent /F`，再删除
`C:\ProgramData\RelayAgent`。
