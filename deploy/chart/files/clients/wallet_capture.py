"""Finite wallet capture workloads. The chart supplies this to client-python:v0.3.0."""
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

MIB = 1 << 20
NONCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}\Z")


def markers(path):
    result = {}
    for line in Path(path).read_text().splitlines():
        columns = line.split("\t")
        if len(columns) >= 2 and columns[0] != "masked":
            value = columns[1]
            result[value.removeprefix("KSKEEP-WALLET-")] = value
    return result


def statement(values, suffix, size):
    # Byte-for-byte wallet.Statement, including the password beyond the preview.
    head = ('{"description":"' + values["STATEMENT-" + suffix] + '","lines":"').encode()
    tail = ('","password":"' + values["STATEMENTPASSWORD-" + suffix] + '"}').encode()
    return head + b"x" * (size - len(head) - len(tail)) + tail


def exchange(connection, method, path, body=None, headers=None):
    connection.request(method, path, body=body, headers=headers or {})
    response = connection.getresponse()
    raw = response.read()
    if response.status != 200:
        raise ValueError(f"{method} {urlsplit(path).path}: HTTP {response.status}")
    return raw


class Controller:
    def __init__(self, wallet, gateway, values, gateway_pod):
        self.wallet = wallet
        self.gateway = gateway
        self.values = values
        self.gateway_pod = gateway_pod
        # Allocate and hash before any tested socket is opened.
        self.bodies = (statement(values, "UNDER", MIB), statement(values, "OVER", 3 * MIB))
        self.hashes = tuple(hashlib.sha256(body).hexdigest() for body in self.bodies)
        self.lock = threading.Lock()
        self.used = set()

    def health(self):
        # Dedicated connections: never warm up the gateway's wallet connection.
        for address in (self.wallet, self.gateway):
            connection = http.client.HTTPConnection(address, timeout=5)
            try:
                exchange(connection, "GET", "/healthz")
            finally:
                connection.close()

    def run(self, mode, nonce):
        if mode not in ("wallet", "burst") or not NONCE.fullmatch(nonce):
            return 400, {"error": "invalid mode or run nonce"}
        if not self.lock.acquire(blocking=False):
            return 409, {"error": "a run is active"}
        ledger = {"schema_version": 1, "mode": mode, "run": nonce,
                  "gateway_pod": self.gateway_pod, "started_unix_nano": time.time_ns(),
                  "exchanges": [], "upload_bytes": 0}
        try:
            if nonce in self.used:
                return 409, {"error": "run nonce already used; runs are never replayed"}
            # Consume failed nonces too: callers must not retry an uncertain run.
            self.used.add(nonce)
            self.health()
            if mode == "wallet":
                self.run_wallet(ledger)
            else:
                self.run_burst(ledger)
            status = 200
        except (OSError, ValueError, KeyError, TypeError, http.client.HTTPException):
            # Do not echo a potentially sensitive app response or exception text.
            ledger["error"] = "workload response or connection validation failed"
            status = 502
        finally:
            ledger["finished_unix_nano"] = time.time_ns()
            self.lock.release()
        return status, ledger

    def run_wallet(self, ledger):
        expected = (
            ("slow", (("statement under budget", f"{MIB} bytes accepted", "POST", "/statements"),
                      ("statement over budget", f"{3 * MIB} bytes accepted", "POST", "/statements"))),
            ("ok", (("sign in", f'signed in as {self.values["USER-ERIN"]}, 1 cookie(s), Bearer token for 300s', "POST", "/session"),
                    ("profile", "profile of " + self.values["NAME-ERIN"], "GET", "/profile"),
                    ("card payment", "approved, card ending 8298", "POST", "/payments"),
                    ("form payment", "approved by form", "POST", "/payments/form"))),
        )
        connection = http.client.HTTPConnection(self.gateway, timeout=60)
        try:
            for variant, steps in expected:
                raw = exchange(connection, "POST", "/api/flows/wallet/run?variant=" + variant)
                result = json.loads(raw)
                if (not isinstance(result, dict) or not isinstance(result.get("steps"), list)
                        or result.get("flow") != "wallet" or result.get("variant") != variant
                        or result.get("error") or len(result["steps"]) != len(steps)):
                    raise ValueError("invalid gateway run")
                for actual, (name, detail, method, path) in zip(result["steps"], steps):
                    if (not isinstance(actual, dict) or actual.get("name") != name or actual.get("detail") != detail
                            or actual.get("protocol") != "http" or actual.get("error")
                            or actual.get("target") != "http://" + self.wallet):
                        raise ValueError("invalid gateway step")
                    sequence = len(ledger["exchanges"])
                    entry = {"sequence": sequence, "method": method, "path": path, "status": 200}
                    if variant == "slow":
                        size = len(self.bodies[sequence])
                        entry.update(request_bytes=size, request_sha256=self.hashes[sequence], accepted_bytes=size)
                        ledger["upload_bytes"] += size
                    elif method == "GET":
                        entry["request_bytes"] = 0
                    ledger["exchanges"].append(entry)
        finally:
            connection.close()

    def run_burst(self, ledger):
        connection = http.client.HTTPConnection(self.wallet, timeout=30)
        try:
            connection.connect()
            socket = connection.sock
            # Initial profile, then 1 MiB + six 3 MiB uploads, each followed by a profile.
            plan = [None]
            for index in (0, 1, 1, 1, 1, 1, 1):
                plan.extend((index, None))
            for sequence, index in enumerate(plan):
                if connection.sock is not socket:
                    raise ValueError("keepalive connection changed")
                headers = {"X-Perfshark-Run": ledger["run"], "X-Perfshark-Sequence": str(sequence),
                           "X-Demo-User": self.values["USER-ERIN"]}
                body = None
                method, path = "GET", "/profile"
                if index is None:
                    headers.update({"X-API-Key": self.values["APIKEY-ERIN"],
                                    "Authorization": "Bearer " + self.values["BEARER-ERIN"],
                                    "X-Auth-Token": self.values["XAUTHTOKEN-ERIN"],
                                    "Cookie": "wallet_session=" + self.values["SESSION-ERIN"]})
                else:
                    method, path, body = "POST", "/statements", self.bodies[index]
                    headers["Content-Type"] = "application/json"
                raw = exchange(connection, method, path, body, headers)
                if connection.sock is not socket:
                    raise ValueError("server closed keepalive connection")
                response = json.loads(raw)
                entry = {"sequence": sequence, "method": method, "path": path, "status": 200,
                         "request_bytes": len(body) if body is not None else 0,
                         "response_body": raw.decode("utf-8")}
                if index is None:
                    expected = {"user": self.values["USER-ERIN"], "display_name": self.values["NAME-ERIN"],
                                "ssn": self.values["SSN-ERIN"], "pinned": self.values["PINNED-CARD"],
                                "tokenizer": self.values["TOKENIZER-V2"]}
                    if response != expected:
                        raise ValueError("invalid profile")
                else:
                    if response != {"accepted_bytes": len(body)} or type(response["accepted_bytes"]) is not int:
                        raise ValueError("incorrect accepted byte count")
                    entry.update(accepted_bytes=response["accepted_bytes"], request_sha256=self.hashes[index])
                    ledger["upload_bytes"] += len(body)
                ledger["exchanges"].append(entry)
        finally:
            connection.close()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        pass

    def reply(self, status, value):
        body = json.dumps(value, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(body)
        self.close_connection = True

    def do_GET(self):
        if self.path != "/readyz":
            self.reply(404, {"error": "unknown path"})
            return
        try:
            self.server.controller.health()
        except (OSError, ValueError, http.client.HTTPException):
            self.reply(503, {"error": "wallet or gateway is not healthy"})
            return
        self.reply(200, {"ready": True})

    def do_POST(self):
        url = urlsplit(self.path)
        query = parse_qs(url.query, keep_blank_values=True)
        if (url.path != "/run" or set(query) != {"mode", "run"}
                or any(len(values) != 1 for values in query.values())
                or self.headers.get("Transfer-Encoding")
                or self.headers.get("Content-Length", "0") != "0"):
            self.reply(400, {"error": "expected POST /run?mode=wallet|burst&run=<nonce> without a body"})
            return
        self.reply(*self.server.controller.run(query["mode"][0], query["run"][0]))


def main():
    controller = Controller(os.environ["WALLET_ADDRESS"], os.environ["GATEWAY_ADDRESS"],
                            markers(os.environ["WALLET_MARKERS"]), os.environ["POD_NAME"])
    server = ThreadingHTTPServer(("0.0.0.0", 8081), Handler)
    server.controller = controller
    server.serve_forever()


if __name__ == "__main__":
    main()
