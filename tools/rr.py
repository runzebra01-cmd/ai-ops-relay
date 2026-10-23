#!/usr/bin/env python3
"""通过 Relay 在指定主机上执行 argv 并等待结果。

用法：
  export RELAY="https://relay.example.com"
  export RELAY_API_TOKEN="<控制面令牌>"
  export RELAY_CA="/path/to/ca.pem"      # 自签证书时设置
  python3 tools/rr.py <host> '["tail","-n","100","/var/log/app.log"]' [timeout_ms]
"""

import json
import sys

from relay_client import config, run


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)

    host = sys.argv[1]
    argv = json.loads(sys.argv[2])
    timeout_ms = int(sys.argv[3]) if len(sys.argv) > 3 else 60000
    base, token, ctx = config()
    sys.exit(run(base, token, ctx, host, argv, timeout_ms))


if __name__ == "__main__":
    main()
