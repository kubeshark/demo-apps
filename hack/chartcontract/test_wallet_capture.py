"""Isolated HTTP contract peers, not replacement application workloads."""
from contextlib import contextmanager
import hashlib
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import threading
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = Path(os.environ.get("CAPTURE_SCRIPT", ROOT / "deploy/chart/files/clients/wallet_capture.py"))
MARKERS = Path(os.environ.get("CAPTURE_MARKERS", ROOT / "deploy/chart/files/markers/wallet.tsv"))
spec = importlib.util.spec_from_file_location("wallet_capture", SCRIPT)
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)
VALUES = capture.markers(MARKERS)


@contextmanager
def serve(handler):
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01})
    thread.start()
    try:
        yield server, "%s:%s" % server.server_address
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


class Peer(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_args):
        pass

    def do_GET(self):
        self.handle_request()

    def do_POST(self):
        self.handle_request()

    def handle_request(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.server.calls.append((self.command, self.path, body, dict(self.headers), self.client_address))
        status, value = self.server.answer(self.command, self.path, body, self.headers)
        raw = value if isinstance(value, bytes) else json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Length", str(len(raw)))
        if getattr(self.server, "close_after", False) and self.path != "/healthz":
            self.send_header("Connection", "close")
            self.close_connection = True
        self.end_headers()
        self.wfile.write(raw)


def profile():
    return {"user": VALUES["USER-ERIN"], "display_name": VALUES["NAME-ERIN"],
            "ssn": VALUES["SSN-ERIN"], "pinned": VALUES["PINNED-CARD"],
            "tokenizer": VALUES["TOKENIZER-V2"]}


def answer(method, path, body, headers):
    if path == "/healthz":
        return 200, b""
    if path == "/profile":
        if (headers.get("X-API-Key") != VALUES["APIKEY-ERIN"]
                or headers.get("Cookie") != "wallet_session=" + VALUES["SESSION-ERIN"]):
            return 401, {"error": "invalid demo credentials"}
        return 200, profile()
    if path == "/statements":
        return 200, {"accepted_bytes": len(body)}
    raise AssertionError((method, path))


class CaptureTests(unittest.TestCase):
    @contextmanager
    def controller(self):
        with serve(Peer) as (peer, address):
            peer.calls = []
            peer.answer = answer
            controller = capture.Controller(address, address, VALUES, "test-client")
            yield controller, peer

    def test_statement_matches_wallet_shape_and_exact_tail(self):
        for suffix, size in (("UNDER", 1 << 20), ("OVER", 3 << 20)):
            body = capture.statement(VALUES, suffix, size)
            value = json.loads(body)
            self.assertEqual(size, len(body))
            self.assertEqual({"description", "lines", "password"}, set(value))
            self.assertEqual("KSKEEP-WALLET-STATEMENT-" + suffix, value["description"])
            self.assertEqual("KSKEEP-WALLET-STATEMENTPASSWORD-" + suffix, value["password"])
            self.assertGreater(body.index(b'"password"'), size - 100)
            self.assertEqual({"x"}, set(value["lines"]))

    def test_burst_exact_order_bytes_connection_and_real_ledger(self):
        with self.controller() as (controller, peer):
            # Any added pacing must fail rather than make this test slower.
            with patch.object(capture.time, "sleep", side_effect=AssertionError("pacing")), \
                    patch.object(capture, "statement", side_effect=AssertionError("late body allocation")), \
                    patch.object(capture.hashlib, "sha256", side_effect=AssertionError("late body hashing")):
                status, ledger = controller.run("burst", "burst-1")
            self.assertEqual(200, status, ledger)
            calls = [call for call in peer.calls if call[1] != "/healthz"]
            expected = [("GET", "/profile")]
            for _ in range(7):
                expected += [("POST", "/statements"), ("GET", "/profile")]
            self.assertEqual(expected, [(call[0], call[1]) for call in calls])
            self.assertEqual(1, len({call[4] for call in calls}))
            self.assertFalse({call[4] for call in calls} & {call[4] for call in peer.calls if call[1] == "/healthz"})
            self.assertEqual(19 << 20, ledger["upload_bytes"])
            self.assertEqual(15, len(ledger["exchanges"]))
            self.assertGreaterEqual(ledger["finished_unix_nano"], ledger["started_unix_nano"])
            for sequence, (call, entry) in enumerate(zip(calls, ledger["exchanges"])):
                self.assertEqual(str(sequence), call[3]["X-Perfshark-Sequence"])
                self.assertEqual("burst-1", call[3]["X-Perfshark-Run"])
                self.assertEqual(sequence, entry["sequence"])
                self.assertEqual(call[0:2], (entry["method"], entry["path"]))
                self.assertEqual(200, entry["status"])
                expected_size = 0 if sequence % 2 == 0 else (1 if sequence == 1 else 3) << 20
                self.assertEqual(expected_size, len(call[2]))
                self.assertEqual(expected_size, entry["request_bytes"])
                if expected_size:
                    self.assertEqual(hashlib.sha256(call[2]).hexdigest(), entry["request_sha256"])
                    self.assertEqual(expected_size, entry["accepted_bytes"])
                    self.assertEqual({"accepted_bytes": expected_size}, json.loads(entry["response_body"]))
                else:
                    self.assertEqual("Bearer " + VALUES["BEARER-ERIN"], call[3]["Authorization"])
                    self.assertEqual(VALUES["XAUTHTOKEN-ERIN"], call[3]["X-Auth-Token"])
                    self.assertEqual(profile(), json.loads(entry["response_body"]))

    def test_burst_rejects_wrong_size_profile_status_json_and_connection(self):
        for defect in ("size", "profile", "status", "json", "closed"):
            with self.subTest(defect=defect), self.controller() as (controller, peer):
                def broken(method, path, body, headers):
                    status, value = answer(method, path, body, headers)
                    if path != "/healthz":
                        if defect == "size" and path == "/statements":
                            value = {"accepted_bytes": len(body) - 1}
                        elif defect == "profile":
                            value = dict(profile(), display_name="wrong")
                        elif defect == "status":
                            status = 401
                        elif defect == "json":
                            value = b"not-json"
                    return status, value
                peer.answer = broken
                peer.close_after = defect == "closed"
                status, ledger = controller.run("burst", "broken")
                self.assertEqual(502, status)
                self.assertIn("error", ledger)
                workload = [call for call in peer.calls if call[1] != "/healthz"]
                self.assertEqual(2 if defect == "size" else 1, len(workload))
                self.assertEqual(409, controller.run("burst", "broken")[0])
                self.assertEqual(len(workload), len([call for call in peer.calls if call[1] != "/healthz"]))

    def gateway_answer(self, address, defect=None):
        def gateway(method, path, body, headers):
            if path == "/healthz":
                return 200, b""
            variant = path.rsplit("=", 1)[1]
            steps = [("statement under budget", "1048576 bytes accepted"),
                     ("statement over budget", "3145728 bytes accepted")] if variant == "slow" else [
                ("sign in", "signed in as KSKEEP-WALLET-USER-ERIN, 1 cookie(s), Bearer token for 300s"),
                ("profile", "profile of KSKEEP-WALLET-NAME-ERIN"),
                ("card payment", "approved, card ending 8298"), ("form payment", "approved by form")]
            result = {"flow": "wallet", "variant": variant,
                      "steps": [{"name": name, "detail": detail, "protocol": "http", "target": "http://" + address}
                                for name, detail in steps]}
            if defect == "order":
                result["steps"].reverse()
            elif defect == "missing":
                result["steps"].pop()
            elif defect == "size":
                result["steps"][0]["detail"] = "1 bytes accepted"
            elif defect == "step-error":
                result["steps"][0]["error"] = "failure"
            elif defect == "run-error":
                result["error"] = "failure"
            elif defect == "variant":
                result["variant"] = "error"
            elif defect == "target":
                result["steps"][0]["target"] = "http://wrong-wallet:8080"
            elif defect == "not-object":
                result = []
            elif defect == "not-steps":
                result["steps"] = None
            elif defect == "not-step":
                result["steps"][0] = None
            return 200, result
        return gateway

    def test_wallet_uses_actual_gateway_control_path_and_validated_steps(self):
        with self.controller() as (controller, peer):
            peer.answer = self.gateway_answer(controller.wallet)
            with patch.object(capture.time, "sleep", side_effect=AssertionError("pacing")):
                status, ledger = controller.run("wallet", "wallet-1")
            self.assertEqual(200, status, ledger)
            self.assertEqual([("POST", "/api/flows/wallet/run?variant=slow"),
                              ("POST", "/api/flows/wallet/run?variant=ok")],
                             [(call[0], call[1]) for call in peer.calls if call[1] != "/healthz"])
            self.assertEqual(["/statements", "/statements", "/session", "/profile", "/payments", "/payments/form"],
                             [entry["path"] for entry in ledger["exchanges"]])
            self.assertEqual(list(range(6)), [entry["sequence"] for entry in ledger["exchanges"]])
            self.assertEqual(4 << 20, ledger["upload_bytes"])
            for index in (0, 1):
                entry = ledger["exchanges"][index]
                self.assertEqual(len(controller.bodies[index]), entry["accepted_bytes"])
                self.assertEqual(controller.hashes[index], entry["request_sha256"])
            self.assertNotIn("request_bytes", ledger["exchanges"][2])
            self.assertEqual(0, ledger["exchanges"][3]["request_bytes"])

    def test_gateway_false_success_never_triggers_second_variant(self):
        for defect in ("order", "missing", "size", "step-error", "run-error", "variant", "target", "not-object", "not-steps", "not-step"):
            with self.subTest(defect=defect), self.controller() as (controller, peer):
                peer.answer = self.gateway_answer(controller.wallet, defect)
                status, ledger = controller.run("wallet", "invalid-gateway")
                self.assertEqual(502, status)
                self.assertIn("error", ledger)
                self.assertEqual(1, len([call for call in peer.calls if call[1] != "/healthz"]))

    def test_gateway_ok_variant_failure_is_not_hidden(self):
        with self.controller() as (controller, peer):
            healthy = self.gateway_answer(controller.wallet)
            def fail_ok(method, path, body, headers):
                status, result = healthy(method, path, body, headers)
                if path.endswith("variant=ok"):
                    result["steps"][-1]["error"] = "form rejected"
                return status, result
            peer.answer = fail_ok
            status, ledger = controller.run("wallet", "failed-ok")
            self.assertEqual(502, status)
            self.assertIn("error", ledger)
            self.assertEqual(2, len([call for call in peer.calls if call[1] != "/healthz"]))

    def test_control_validation_readiness_and_exclusion(self):
        with self.controller() as (controller, peer), serve(capture.Handler) as (server, address):
            server.controller = controller
            self.assertEqual([], peer.calls, "constructor must not start traffic")
            for mode, nonce in (("bad", "valid"), ("burst", ""), ("burst", "a/b"), ("burst", "a" * 129)):
                self.assertEqual(400, controller.run(mode, nonce)[0])
            controller.lock.acquire()
            try:
                self.assertEqual(409, controller.run("wallet", "busy")[0])
            finally:
                controller.lock.release()
            self.assertEqual([], peer.calls)
            connection = http.client.HTTPConnection(address, timeout=5)
            connection.request("GET", "/readyz")
            response = connection.getresponse()
            self.assertEqual(200, response.status)
            response.read()
            connection.close()
            self.assertEqual(["/healthz", "/healthz"], [call[1] for call in peer.calls])
            peer.answer = lambda *_args: (503, {})
            for method, path, expected in (("GET", "/readyz", 503),
                                           ("POST", "/run?mode=burst&run=x", 502),
                                           ("POST", "/run?mode=burst&mode=wallet&run=y", 400),
                                           ("POST", "/run?mode=burst&run=z&extra=1", 400)):
                connection = http.client.HTTPConnection(address, timeout=5)
                connection.request(method, path)
                response = connection.getresponse()
                self.assertEqual(expected, response.status)
                self.assertIn("error", json.loads(response.read()))
                connection.close()
            self.assertTrue(all(call[1] == "/healthz" for call in peer.calls))



if __name__ == "__main__":
    unittest.main()
