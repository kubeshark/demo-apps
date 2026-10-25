"""Calls a flow through the gateway over HTTPS and queries postgresql over TLS on an
interval, so TLS through OpenSSL (Python's ssl module and libpq) shows up next to Go."""

import os
import re
import time
from datetime import datetime, timezone

import psycopg
import requests


def markers(path):
    """Values keyed by FIELD-REST of KSKEEP-<FLOW>-<FIELD>-<REST>."""
    out = {}
    with open(path) as f:
        for line in f:
            cols = line.rstrip("\n").split("\t")
            if len(cols) >= 3:
                parts = cols[1].split("-", 3)
                if len(parts) == 4:
                    out[f"{parts[2]}-{parts[3]}"] = cols[1]
    return out


def interval(text):
    m = re.fullmatch(r"(\d+)(ms|s|m)", text)
    if not m:
        raise SystemExit(f"bad INTERVAL {text!r}")
    return int(m[1]) * {"ms": 0.001, "s": 1, "m": 60}[m[2]]


def log(step, msg):
    print(f"{datetime.now(timezone.utc).isoformat()} {step} {msg}", flush=True)


def main():
    m = markers("/app/markers.tsv")
    agent, literal = m["AGENT-PYTHON"], m["QUERY-PYTHON"]
    ca = os.environ.get("CA_FILE", "/etc/demo-apps/tls/ca.crt")
    gateway = os.environ.get("GATEWAY_URL", "https://demo-apps-gateway")
    flow = os.environ.get("FLOW", "orders")
    every = interval(os.environ.get("INTERVAL", "15s"))
    host, _, port = os.environ.get("POSTGRES_ADDR", "demo-apps-postgresql:5432").partition(":")
    dsn = dict(
        host=host,
        port=port or "5432",
        dbname=os.environ.get("POSTGRES_DB", "demo"),
        user=os.environ.get("POSTGRES_USER", "demo"),
        password=os.environ["POSTGRES_PASSWORD"],
        sslmode="verify-full",
        sslrootcert=ca,
        application_name=agent,
    )

    while True:
        try:
            # A new session per run, so the TLS handshake recurs.
            with requests.Session() as s:
                r = s.post(
                    f"{gateway}/api/flows/{flow}/run",
                    params={"source": "python"},
                    headers={"X-Demo-Client": agent},
                    verify=ca,
                    timeout=30,
                )
            log("https", f"flow={flow} status={r.status_code}")
        except requests.RequestException as e:
            log("https", f"error={e}")
        try:
            with psycopg.connect(**dsn) as conn:
                row = conn.execute("SELECT %s::text AS marker, version()", (literal,)).fetchone()
            log("postgresql", f"selected={row[0]}")
        except psycopg.Error as e:
            log("postgresql", f"error={e}")
        time.sleep(every)


if __name__ == "__main__":
    main()
