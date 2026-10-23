#!/usr/bin/env bash
# 临时跳板机：前台运行 relay-agent，Ctrl+C 或关掉终端立即断开，不留后台服务。
#
# 用法：
#   export RELAY="https://relay.example.com"
#   export RELAY_AGENT_TOKEN="<主机令牌>"
#   ./start-ubuntu-jump.sh -host ubuntu-jump [-ca /path/ca.pem] [-insecure]
#
# 说明：
#   - 令牌只从环境变量或命令行读取，脚本里不保存任何真实令牌。
#   - 自签证书时用 -ca 指定 ca.pem；-insecure 仅用于临时测试。
set -euo pipefail

RELAY="${RELAY:-}"
HOSTID="${HOSTID:-ubuntu-jump}"
TOKEN="${RELAY_AGENT_TOKEN:-}"
CA="${RELAY_CA:-}"
DL_TOKEN="${RELAY_DOWNLOAD_TOKEN:-}"
INSECURE=0
DIR="${RELAY_TMPDIR:-/tmp/relay-agent}"

usage() {
  sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    -relay)          RELAY="${2:-}"; shift 2 ;;
    -host)           HOSTID="${2:-}"; shift 2 ;;
    -token)          TOKEN="${2:-}"; shift 2 ;;
    -ca)             CA="${2:-}"; shift 2 ;;
    -download-token) DL_TOKEN="${2:-}"; shift 2 ;;
    -insecure)       INSECURE=1; shift ;;
    -h|--help)       usage; exit 0 ;;
    *) echo "未知参数: $1"; usage; exit 1 ;;
  esac
done

if [ -z "$RELAY" ] || [ -z "$TOKEN" ]; then
  echo "错误: 需要 RELAY 和令牌（环境变量或 -relay/-token）"
  usage
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "缺少 curl，请先安装：sudo apt install -y curl"
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构: $(uname -m)"; exit 1 ;;
esac

mkdir -p "$DIR" && cd "$DIR"

echo "== 准备 agent =="
if [ ! -x relay-agent ]; then
  URL="$RELAY/dl/relay-agent-linux-$ARCH"
  if [ -n "$DL_TOKEN" ]; then
    URL="$URL?k=$DL_TOKEN"
  fi
  curl -fsSL "$URL" -o relay-agent.tmp
  chmod 700 relay-agent.tmp && mv relay-agent.tmp relay-agent
fi

ARGS=(-relay "$RELAY" -host "$HOSTID" -token "$TOKEN")
if [ -n "$CA" ]; then
  ARGS+=(-ca "$CA")
elif [ -f "$DIR/ca.pem" ]; then
  ARGS+=(-ca "$DIR/ca.pem")
elif [ "$INSECURE" -eq 1 ]; then
  ARGS+=(-insecure)
else
  echo "提示: 未找到 ca.pem，将按系统 CA 校验；如果 Relay 用自签证书，请加 -ca /path/ca.pem"
fi

echo "== 启动（这个窗口就是开关，关掉即下线）=="
exec ./relay-agent "${ARGS[@]}"
