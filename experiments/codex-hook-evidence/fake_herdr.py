#!/usr/bin/env python3
"""Test-owned Herdr socket that records SessionStart reports."""

from __future__ import annotations

import argparse
import json
import os
import socket
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", required=True)
    parser.add_argument("--log", required=True)
    args = parser.parse_args()

    socket_path = Path(args.socket)
    log_path = Path(args.log)
    if socket_path.exists():
        socket_path.unlink()
    socket_path.parent.mkdir(parents=True, exist_ok=True)
    log_path.parent.mkdir(parents=True, exist_ok=True)

    server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    server.bind(os.fspath(socket_path))
    server.listen()
    try:
        while True:
            connection, _ = server.accept()
            with connection:
                raw = b""
                while not raw.endswith(b"\n"):
                    chunk = connection.recv(4096)
                    if not chunk:
                        break
                    raw += chunk
                if raw.strip():
                    request = json.loads(raw)
                    with log_path.open("a", encoding="utf-8") as handle:
                        handle.write(json.dumps(request, sort_keys=True) + "\n")
                    response = {"id": request.get("id"), "result": {}}
                    connection.sendall((json.dumps(response) + "\n").encode())
    except KeyboardInterrupt:
        return 0
    finally:
        server.close()
        if socket_path.exists():
            socket_path.unlink()


if __name__ == "__main__":
    raise SystemExit(main())
