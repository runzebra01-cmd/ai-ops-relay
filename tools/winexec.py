#!/usr/bin/env python3
"""在 Windows 主机上执行 argv，并补齐 OpenSSH 常用的用户环境变量。

有些 Windows 机器上的 ssh.exe 在缺少 APPDATA / USERPROFILE 时会静默退出
（exit 255 且没有输出），这个脚本用于给被执行的命令补上这些变量。

用法：
  export RELAY="https://relay.example.com"
  export RELAY_API_TOKEN="<控制面令牌>"
  export WIN_USER="user"                  # 可选，默认 user
  export WIN_PROFILE='C:\\Users\\user'    # 可选，默认按 WIN_USER 推导
  python3 tools/winexec.py <host> '["ssh","-V"]' [timeout_ms]
"""

import os
import sys
import json

from relay_client import config, run


def windows_env():
    user = os.environ.get("WIN_USER", "user")
    profile = os.environ.get("WIN_PROFILE", r"C:\Users\%s" % user)
    return {
        "APPDATA": profile + r"\AppData\Roaming",
        "LOCALAPPDATA": profile + r"\AppData\Local",
        "USERNAME": user,
        "USERPROFILE": profile,
        "HOMEDRIVE": "C:",
        "HOMEPATH": r"\Users\%s" % user,
        "ProgramData": r"C:\ProgramData",
        "ALLUSERSPROFILE": r"C:\ProgramData",
        "ProgramFiles": r"C:\Program Files",
        "ProgramFiles(x86)": r"C:\Program Files (x86)",
    }


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)

    host = sys.argv[1]
    argv = json.loads(sys.argv[2])
    timeout_ms = int(sys.argv[3]) if len(sys.argv) > 3 else 60000
    base, token, ctx = config()
    sys.exit(run(base, token, ctx, host, argv, timeout_ms, env=windows_env()))


if __name__ == "__main__":
    main()
