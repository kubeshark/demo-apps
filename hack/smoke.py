"""Run every registered flow in every variant against a gateway URL."""

import json
import sys
import urllib.request

base = sys.argv[1]
flows = json.load(urllib.request.urlopen(f"{base}/api/flows"))
if not flows:
    sys.exit("no flows registered")

failed = False
for flow in flows:
    for variant in ("ok", "slow", "error"):
        req = urllib.request.Request(
            f"{base}/api/flows/{flow['name']}/run?variant={variant}&source=smoke", method="POST"
        )
        try:
            with urllib.request.urlopen(req) as resp:
                r = json.load(resp)
        except urllib.error.HTTPError as e:
            print(f"FAIL {flow['name']}/{variant}: HTTP {e.code} {e.read().decode()}")
            failed = True
            continue
        steps = ", ".join(
            f"{s['protocol']} {s['name']} {s['duration'] / 1e6:.0f}ms ({s.get('detail', '')})" for s in r["steps"]
        )
        print(f"ok   {flow['name']}/{variant}: {steps}")

sys.exit(1 if failed else 0)
