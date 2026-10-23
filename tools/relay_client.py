"""Relay 控制面 API 的最小 Python 客户端，供 tools/ 下的示例脚本复用。

环境变量：
  RELAY              Relay 地址，例如 https://relay.example.com（必填）
  RELAY_API_TOKEN    控制面 API 令牌（必填，不要写进代码或提交到仓库）
  RELAY_CA           自签证书的 ca.pem 路径（可选）
  RELAY_INSECURE=1   跳过 TLS 校验（仅本地测试，可选）
"""

import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request


def config():
    base = os.environ.get("RELAY", "").rstrip("/")
    token = os.environ.get("RELAY_API_TOKEN", "")
    if not base or not token:
        sys.exit("错误: 请先设置环境变量 RELAY 和 RELAY_API_TOKEN")

    ca = os.environ.get("RELAY_CA", "")
    ctx = ssl.create_default_context(cafile=ca) if ca else ssl.create_default_context()
    if os.environ.get("RELAY_INSECURE") == "1":
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
    return base, token, ctx


def call(base, token, ctx, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        base + path,
        data=data,
        method=method,
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=30) as resp:
            return json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode(errors="replace")[:300]
        sys.exit("HTTP %d: %s" % (exc.code, detail))


def run(base, token, ctx, host, argv, timeout_ms=60000, env=None, cwd=None):
    """创建任务并轮询到结束，打印结果后返回退出码。"""
    body = {"host_id": host, "args": argv, "timeout_ms": timeout_ms}
    if env:
        body["env"] = env
    if cwd:
        body["cwd"] = cwd

    task = call(base, token, ctx, "POST", "/v1/tasks", body)
    if "error" in task:
        sys.exit("ERR: " + task["error"])

    task_id = task["task"]["id"]
    deadline = time.time() + timeout_ms / 1000 + 30
    while time.time() < deadline:
        time.sleep(1)
        view = call(base, token, ctx, "GET", "/v1/tasks/" + task_id)
        if view["state"] not in ("done", "error"):
            continue

        result = view["result"]
        print("exit=%d%s" % (
            result["exit_code"],
            " [truncated]" if result.get("truncated") else "",
        ))
        if result.get("err"):
            print("err:", result["err"])
        if (result.get("stdout") or "").strip():
            print(result["stdout"].rstrip())
        if (result.get("stderr") or "").strip():
            print("--- stderr ---\n" + result["stderr"].rstrip())
        return 0

    print("TIMEOUT")
    return 2
