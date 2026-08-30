#!/usr/bin/env python3
"""Capture one Codex hook invocation as a JSON line for ATC-281."""

from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path


def main() -> int:
    capture_path = os.environ.get("ATC_HOOK_CAPTURE_FILE")
    test_id = os.environ.get("ATC_HOOK_TEST_ID")
    if not capture_path or not test_id:
        return 0

    payload = json.load(sys.stdin)
    record = {
        "captured_at_ns": time.time_ns(),
        "test_id": test_id,
        "scenario": os.environ.get("ATC_HOOK_SCENARIO"),
        "event": payload.get("hook_event_name"),
        "codex_thread_id": os.environ.get("CODEX_THREAD_ID"),
        "atc_env": {
            key: value
            for key, value in os.environ.items()
            if key.startswith("ATC_") and key != "ATC_HOOK_CAPTURE_FILE"
        },
        "payload": payload,
    }
    path = Path(capture_path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(record, sort_keys=True) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
