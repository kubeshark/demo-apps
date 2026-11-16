"""Generate the chart-owned synthetic DLT_RAW IPv4/TCP snapshot seed.
Run from any directory: python3 hack/snapshot_seed.py
Only Python's standard library is required. No capture or external input is read.
"""

import argparse
import hashlib
import ipaddress
import json
import pathlib
import struct


ROOT = pathlib.Path(__file__).resolve().parent.parent
DEFAULT_OUTPUT = ROOT / "deploy/chart/files/seeds/snapshot.pcap"
EPOCH = 1704067200
VARIANTS = (
    ("GET", "catalog", 200, "OK", "synthetic-ok"),
    ("POST", "orders", 201, "Created", "synthetic-created"),
    ("PUT", "stock", 200, "OK", "synthetic-updated"),
    ("DELETE", "missing", 404, "Not Found", "synthetic-not-found"),
    ("POST", "reject", 422, "Unprocessable Entity", "synthetic-invalid-quantity"),
)


def checksum(data):
    if len(data) % 2:
        data += b"\x00"
    total = sum(struct.unpack(f"!{len(data) // 2}H", data))
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (~total) & 0xFFFF


def packet(src, dst, sport, dport, seq, ack, flags, payload, ident):
    tcp = struct.pack("!HHIIBBHHH", sport, dport, seq, ack, 0x50, flags, 32768, 0, 0)
    pseudo = src + dst + struct.pack("!BBH", 0, 6, len(tcp) + len(payload))
    tcp = tcp[:16] + struct.pack("!H", checksum(pseudo + tcp + payload)) + tcp[18:]
    ip = struct.pack("!BBHHHBBH4s4s", 0x45, 0, 40 + len(payload), ident, 0x4000, 64, 6, 0, src, dst)
    ip = ip[:10] + struct.pack("!H", checksum(ip)) + ip[12:]
    return ip + tcp + payload


def body(number, operation, count, result=None):
    value = {
        "synthetic": True,
        "sequence": number,
        "operation": operation,
        "items": [
            {"sku": f"synthetic-item-{item:03d}", "quantity": (number + item) % 9 + 1}
            for item in range(1, count + 1)
        ],
    }
    if result is not None:
        value["result"] = result
    return json.dumps(value, separators=(",", ":"), sort_keys=True).encode("ascii") + b"\n"


def generate():
    out = bytearray(struct.pack("<IHHIIII", 0xA1B2C3D4, 2, 4, 0, 0, 65535, 101))
    ident = 0
    for number in range(1, 26):
        method, operation, status, reason, result = VARIANTS[(number - 1) % len(VARIANTS)]
        request_body = body(number, operation, number % 4 + 1) if method in ("POST", "PUT") else b""
        request = (
            f"{method} /synthetic/{operation}/{number:03d} HTTP/1.1\r\n"
            "Host: snapshot.demo.invalid\r\n"
            "User-Agent: demo-apps-synthetic-seed/1\r\n"
            "Accept: application/json\r\n"
            "Connection: close\r\n"
            + ("Content-Type: application/json\r\n" if request_body else "")
            + f"Content-Length: {len(request_body)}\r\n\r\n"
        ).encode("ascii") + request_body
        response_body = body(number, operation, number % 12 + 1, result)
        response = (
            f"HTTP/1.1 {status} {reason}\r\n"
            "Server: demo-apps-synthetic-seed/1\r\n"
            "Content-Type: application/json\r\n"
            "Connection: close\r\n"
            f"Content-Length: {len(response_body)}\r\n\r\n"
        ).encode("ascii") + response_body
        endpoints = (
            ipaddress.IPv4Address(f"192.0.2.{number + 10}").packed,
            ipaddress.IPv4Address(f"198.51.100.{number % 3 + 10}").packed,
        )
        ports = (40000 + number, 8080)
        seq = [10000 + number * 1000, 90000 + number * 1000]
        tick = 0

        def emit(direction, flags, payload=b""):
            nonlocal ident, tick
            opposite = 1 - direction
            ack = seq[opposite] if flags & 0x10 else 0
            raw = packet(endpoints[direction], endpoints[opposite], ports[direction], ports[opposite],
                         seq[direction], ack, flags, payload, ident)
            out.extend(struct.pack("<IIII", EPOCH + number - 1, tick * 1000, len(raw), len(raw)))
            out.extend(raw)
            seq[direction] += len(payload) + bool(flags & 0x03)
            ident += 1
            tick += 1

        emit(0, 0x02)
        emit(1, 0x12)
        emit(0, 0x10)
        for direction, message in enumerate((request, response)):
            # Split headers as well as bodies so replay must preserve TCP segment order.
            for chunk in (message[:47], *(message[offset:offset + 512] for offset in range(47, len(message), 512))):
                emit(direction, 0x18, chunk)
            emit(1 - direction, 0x10)
        emit(0, 0x11)
        emit(1, 0x11)
        emit(0, 0x10)
    return bytes(out)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=pathlib.Path, default=DEFAULT_OUTPUT)
    args = parser.parse_args()
    seed = generate()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_bytes(seed)
    print(f"{hashlib.sha256(seed).hexdigest()}  {len(seed)} bytes  {args.output}")


if __name__ == "__main__":
    main()
