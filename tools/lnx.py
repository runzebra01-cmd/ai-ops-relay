#!/usr/bin/env python3
"""通过 Relay 上的跳板主机执行一条 SSH 命令。

用法：
  export RELAY="https://relay.example.com"
  export RELAY_API_TOKEN="<控制面令牌>"
  export SSH_KEY="$HOME/.ssh/relay_jump"   # 可选，跳板机上的私钥路径
  export SSH_PORT="22"                     # 可选，目标端口
  python3 tools/lnx.py <jump-host> <user@target> '<remote command>'

前提：Relay 的策略文件里给 <jump-host> 配好 ssh.key / ssh.cidrs / ssh.ports。
"""

import os
import sys

from relay_client import config, run


def main():
    if len(sys.argv) < 4:
        sys.exit(__doc__)

    jump_host, target, command = sys.argv[1], sys.argv[2], sys.argv[3]
    key = os.environ.get("SSH_KEY", os.path.expanduser("~/.ssh/relay_jump"))
    port = os.environ.get("SSH_PORT", "22")
    ssh_bin = os.environ.get("SSH_BIN", "ssh")

    argv = [
        ssh_bin,
        "-i", key,
        "-o", "BatchMode=yes",
        "-o", "StrictHostKeyChecking=accept-new",
        "-p", port,
        target,
        command,
    ]
    base, token, ctx = config()
    sys.exit(run(base, token, ctx, jump_host, argv))


if __name__ == "__main__":
    main()
