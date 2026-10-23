#!/usr/bin/env bash
# 内网机器安装脚本：装 relay-agent 并常驻。
#
#   sudo bash install.sh -relay https://relay.example.com -host intranet-01 -token <令牌>
#
# 可选：-proxy http://proxy.corp:3128   走企业 HTTP 代理
#       -ca /path/ca.pem               指定 Relay 证书
set -euo pipefail

RELAY_URL=""
HOST_ID=""
TOKEN=""
PROXY=""
HERE="$(cd "$(dirname "$0")" && pwd)"
CA_FILE="$HERE/ca.pem"

usage() {
  sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    -relay)  RELAY_URL="${2:-}"; shift 2 ;;
    -host)   HOST_ID="${2:-}";   shift 2 ;;
    -token)  TOKEN="${2:-}";     shift 2 ;;
    -proxy)  PROXY="${2:-}";     shift 2 ;;
    -ca)     CA_FILE="${2:-}";   shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数: $1"; usage; exit 1 ;;
  esac
done

if [ -z "$RELAY_URL" ] || [ -z "$HOST_ID" ] || [ -z "$TOKEN" ]; then
  echo "错误: -relay / -host / -token 都是必填"
  usage
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "错误: 请用 root 或 sudo 运行"
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "错误: 不支持的架构 $(uname -m)，请联系我编一个"; exit 1 ;;
esac

SRC="$HERE/relay-agent-linux-$ARCH"
if [ ! -f "$SRC" ]; then
  echo "错误: 找不到 $SRC"
  exit 1
fi

echo "== 1. 安装二进制 ($ARCH) =="
install -m 755 "$SRC" /usr/local/bin/relay-agent
relay-agent -version || true

echo "== 2. 安装证书与配置 =="
install -d -m 700 /etc/relay-agent
if [ -f "$CA_FILE" ]; then
  install -m 644 "$CA_FILE" /etc/relay-agent/ca.pem
else
  echo "   警告: 未找到 ca.pem，将使用系统 CA（自签证书会校验失败）"
fi
umask 077
cat > /etc/relay-agent/agent.env <<EOF
RELAY_URL=$RELAY_URL
HOST_ID=$HOST_ID
RELAY_AGENT_TOKEN=$TOKEN
EOF
umask 022

PROXY_LINE=""
if [ -n "$PROXY" ]; then
  PROXY_LINE="Environment=HTTPS_PROXY=$PROXY
Environment=HTTP_PROXY=$PROXY"
fi

echo "== 3. 安装 systemd 服务 =="
cat > /etc/systemd/system/relay-agent.service <<UNIT
[Unit]
Description=Intranet Relay Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/relay-agent/agent.env
$PROXY_LINE
ExecStart=/usr/local/bin/relay-agent \\
  -relay \${RELAY_URL} \\
  -host \${HOST_ID} \\
  -token \${RELAY_AGENT_TOKEN} \\
  -ca /etc/relay-agent/ca.pem
Restart=always
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload

echo "== 4. 先做一次连通性检查 =="
if ! relay-agent -check -relay "$RELAY_URL" -host "$HOST_ID" -token "$TOKEN" -ca /etc/relay-agent/ca.pem; then
  cat <<'TIP'

连接失败。按顺序排查：
  1. 本机能否访问 Relay：curl -k https://<relay-ip>/healthz
  2. 是否必须走代理：加 -proxy http://proxy:port 重跑
  3. 令牌是否抄错（区分大小写，无空格）
  4. 出口防火墙是否只放行 443（本方案默认就用 443）
TIP
  exit 1
fi

echo "== 5. 启动服务 =="
systemctl enable --now relay-agent >/dev/null 2>&1
sleep 2
if systemctl is-active --quiet relay-agent; then
  echo "OK: relay-agent 已启动并注册为 $HOST_ID"
  systemctl status relay-agent --no-pager -l 2>/dev/null | tail -5
else
  echo "服务启动失败："
  journalctl -u relay-agent -n 20 --no-pager
  exit 1
fi
