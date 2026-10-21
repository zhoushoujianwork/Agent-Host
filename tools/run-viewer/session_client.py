"""Stream a continuation through the explicitly selected service's private socket."""

import argparse
import http.client
import json
import os
from pathlib import Path
import socket
import stat
import sys


def main():
    parser = argparse.ArgumentParser()
    for name in ("home", "run", "request-id", "takeover-session", "received-at", "format"):
        parser.add_argument("--" + name, required=name in {"home", "run", "request-id"})
    parser.add_argument("-m", required=True)
    args = parser.parse_args()
    path = Path(args.home) / "memory-control.sock"
    info = path.lstat()
    if not stat.S_ISSOCK(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
        raise ValueError("service control socket is not owner-private")
    connection = http.client.HTTPConnection("localhost", timeout=3600)
    connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    connection.sock.settimeout(3600)
    try:
        connection.sock.connect(str(path))
        connection.request("POST", "/session-chat", json.dumps({"run_id": args.run,
            "request_id": args.request_id, "message": args.m}), {"Content-Type": "application/json"})
        response = connection.getresponse()
        if response.status != 200:
            result = json.loads(response.read(32768))
            print(json.dumps({"error": result.get("error", "Session continuation unavailable")}), flush=True)
            return 1
        while line := response.readline((4 << 20) + 1):
            if len(line) > 4 << 20:
                raise ValueError("service event exceeds bound")
            sys.stdout.buffer.write(line)
            sys.stdout.buffer.flush()
        return 0
    finally:
        connection.close()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, http.client.HTTPException):
        print(json.dumps({"error": "原会话服务不可用或连接已中断，请检查执行记录后再继续。"}), flush=True)
        sys.exit(1)
