#!/usr/bin/env bash
# 发布前检查：扫描“即将提交”的文件（已跟踪 + 未跟踪且未被 .gitignore 忽略），
# 发现私钥、疑似令牌、真实公网 IPv4 时退出码非 0。
#
# 用法：bash scripts/check-secrets.sh
set -uo pipefail
cd "$(dirname "$0")/.."

mapfile -d '' files < <(git ls-files -z -co --exclude-standard)
if [ "${#files[@]}" -eq 0 ]; then
  echo "没有需要扫描的文件。"
  exit 0
fi

fail=0
report() {
  printf '\n\033[31m[!] %s\033[0m\n%s\n' "$1" "$2"
  fail=1
}

# 1) 私钥
hits=$(grep -HnE -- '-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----' "${files[@]}" 2>/dev/null || true)
if [ -n "$hits" ]; then
  report "发现私钥文件内容" "$hits"
fi

# 2) 疑似真实令牌（本项目生成的令牌是 16 字节十六进制 = 32 位）
hits=$(grep -HnE -- '(RELAY_[A-Z_]*TOKEN|RELAY_AGENTS)=[0-9a-fA-F]{16,}|Bearer[[:space:]]+[0-9a-fA-F]{16,}' "${files[@]}" 2>/dev/null || true)
if [ -n "$hits" ]; then
  report "发现疑似真实令牌" "$hits"
fi

# 3) 通用长十六进制串：可能是令牌、哈希或密钥指纹
hits=$(grep -HnE -- '\b[0-9a-fA-F]{32,64}\b' "${files[@]}" 2>/dev/null || true)
if [ -n "$hits" ]; then
  report "发现疑似令牌/密钥的十六进制串" "$hits"
fi

# 4) 明文口令
hits=$(grep -HnE -- '(password|passwd|PASSWORD|PASSWD)["'"'"']?[[:space:]]*[:=][[:space:]]*["'"'"'][^"'"'"']{6,}' "${files[@]}" 2>/dev/null || true)
if [ -n "$hits" ]; then
  report "发现疑似明文口令" "$hits"
fi

# 5) 公网 IPv4（放行回环、私网、链路本地、CGNAT、文档示例段和组播/保留段）
hit=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  ip=${line##*:}
  where=${line%:*}
  verdict=$(printf '%s\n' "$ip" | awk -F. '
    NF != 4 { print "invalid"; exit }
    $1 > 255 || $2 > 255 || $3 > 255 || $4 > 255 { print "invalid"; exit }
    ($1 == 10) || ($1 == 127) || ($1 == 0) { print "private"; exit }
    ($1 == 192 && $2 == 168) { print "private"; exit }
    ($1 == 172 && $2 >= 16 && $2 <= 31) { print "private"; exit }
    ($1 == 100 && $2 >= 64 && $2 <= 127) { print "private"; exit }
    ($1 == 169 && $2 == 254) { print "private"; exit }
    ($1 == 192 && $2 == 0 && $3 == 2) { print "doc"; exit }
    ($1 == 198 && $2 == 51 && $3 == 100) { print "doc"; exit }
    ($1 == 203 && $2 == 0 && $3 == 113) { print "doc"; exit }
    ($1 >= 224) { print "reserved"; exit }
    { print "public" }
  ')
  if [ "$verdict" = "public" ]; then
    printf '%s: 疑似公网 IPv4 %s\n' "$where" "$ip"
    hit=1
    fail=1
  fi
done < <(grep -HnoE -- '([0-9]{1,3}\.){3}[0-9]{1,3}' "${files[@]}" 2>/dev/null || true)
if [ "$hit" -eq 1 ]; then
  printf '\n\033[31m[!] 发现疑似公网 IPv4\033[0m\n'
fi

if [ "$fail" -ne 0 ]; then
  cat <<'TIP'

请删除/替换上述内容后再提交：
  - 公网 IP 改成 relay.example.com 或 192.0.2.10 之类的占位符
  - 令牌改成 <令牌> / REPLACE_ME，并从环境变量读取
  - 私钥、证书、.env、日志全部应由 .gitignore 排除
如果这些内容已经推送过，请立刻轮换令牌并清理 Git 历史。
TIP
  exit 1
fi

echo "OK: 未发现私钥、真实令牌、明文口令或公网 IPv4。"
