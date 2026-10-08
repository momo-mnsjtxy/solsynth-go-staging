#!/usr/bin/env python3
"""Exercise the compiled loopback process against disposable synthetic PostgreSQL.

Run after the integration fixture tests, and again after the PostgreSQL restart.
No external network requests, production credentials or DDL are performed here.
"""
import http.client
import json
import os
import queue
import signal
import subprocess
import sys
import threading
from pathlib import Path
from urllib.parse import urlsplit


def start(binary, synthetic):
    args = [binary, "--listen=127.0.0.1:0"]
    if synthetic:
        if not os.environ.get("PG_TEST_DSN") or os.environ.get("PG_TEST_REQUIRED") != "1":
            raise RuntimeError("disposable PostgreSQL environment is required")
        run = os.environ.get("SOLSYNTH_DISPOSABLE_PG", "")
        expected = f"host={run}/socket port=55432 user=synthetic dbname=postgres sslmode=disable"
        if not run or not Path(run).is_absolute() or not (Path(run) / ".solsynth-disposable").is_file() or os.environ["PG_TEST_DSN"] != expected:
            raise RuntimeError("database must be the marked private-socket integration instance")
        args += ["--synthetic-production-status=42", "--postgres-dsn-env=PG_TEST_DSN"]
    process = subprocess.Popen(args, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
    lines = queue.Queue()

    def drain():
        for line in process.stderr:
            lines.put(line)
        lines.put(None)

    threading.Thread(target=drain, daemon=True).start()
    try:
        for _ in range(10):
            line = lines.get(timeout=10)
            if line is None:
                raise RuntimeError("development process exited before listening (logs suppressed)")
            marker = "Develop discovery listening on "
            if marker in line:
                address = line.split(marker, 1)[1].strip()
                parsed = urlsplit("http://" + address)
                if parsed.hostname != "127.0.0.1" or not parsed.port:
                    raise RuntimeError("unexpected process listen address")
                return process, parsed.port
        raise RuntimeError("development process did not report readiness")
    except BaseException:
        stop(process)
        raise


def stop(process):
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
            raise RuntimeError("development process did not stop gracefully")
    if process.returncode != 0:
        raise RuntimeError("development process failed (logs suppressed)")


def request(port, path, headers=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        connection.request("GET", path, headers=headers or {})
        response = connection.getresponse()
        return response.status, dict(response.getheaders()), json.loads(response.read())
    finally:
        connection.close()


def check(binary):
    process, port = start(binary, False)
    try:
        status, _, data = request(port, "/api/apps")
        assert status == 503 and data == {"error": "discovery_policy_unverified"}
    finally:
        stop(process)
    # Two fresh application processes prove no in-process cache supplies results.
    for _ in range(2):
        process, port = start(binary, True)
        try:
            status, headers, data = request(port, "/api/apps?search=develop-restart-sentinel")
            assert status == 200 and headers.get("X-Total") == "1"
            assert data == [{"id": "96000000-0000-0000-0000-000000000001",
                             "slug": "develop-restart-sentinel", "title": "develop-restart-sentinel",
                             "description": None, "products_count": 0, "widgets_count": 0}]
            status, headers, data = request(port, "/api/apps?take=0&search=develop-restart-sentinel")
            assert status == 200 and headers.get("X-Total") == "1" and data == []
            status, _, _ = request(port, "/api/apps", {"Authorization": "Bearer synthetic-invalid"})
            assert status == 403
        finally:
            stop(process)
    print("Verified real loopback Develop HTTP, default deny, PostgreSQL reads and application restarts")


if __name__ == "__main__":
    try:
        check(sys.argv[1])
    except (AssertionError, OSError, RuntimeError, ValueError, queue.Empty) as error:
        sys.exit(f"Develop HTTP smoke failed: {type(error).__name__}")
