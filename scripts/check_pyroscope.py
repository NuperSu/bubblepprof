#!/usr/bin/env python3
"""Opt-in integration check against a disposable Pyroscope backend.

Uploads a synthetic reachability profile and verifies custom types, selector
labels, byte/count totals, and synthetic flamegraph names via the real query API.
Uses only Python's standard library. Never point this at production test data.
"""
import argparse
import json
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:4040")
    parser.add_argument("--profile", required=True)
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    report = json.loads(Path(args.report).read_text())
    metrics = report["summary"]["metrics"]
    service = "bubblepprof-smoke-" + uuid.uuid4().hex[:12]
    config = {}
    for category in ("reachable", "exclusive", "shared"):
        for suffix, unit in (("space", "bytes"), ("objects", "objects")):
            name = category + "_" + suffix
            config[name] = {"units": unit, "aggregation": "average",
                            "display-name": name, "sampled": False}
    boundary = "bubblepprof-" + uuid.uuid4().hex
    body = bytearray()
    for name, filename, data in (
        ("profile", "reachability.pb.gz", Path(args.profile).read_bytes()),
        ("sample_type_config", "config.json", json.dumps(config).encode()),
    ):
        body.extend((f"--{boundary}\r\nContent-Disposition: form-data; "
                     f'name="{name}"; filename="{filename}"\r\n'
                     "Content-Type: application/octet-stream\r\n\r\n").encode())
        body.extend(data)
        body.extend(b"\r\n")
    body.extend(f"--{boundary}--\r\n".encode())
    now = int(time.time())
    params = urllib.parse.urlencode({"name": service,
                                     "from": now - 1, "until": now})
    request = urllib.request.Request(args.url.rstrip("/") + "/ingest?" + params,
                                    data=bytes(body),
                                    headers={"Content-Type": "multipart/form-data; boundary=" + boundary})
    with urllib.request.urlopen(request, timeout=30) as response:
        assert response.status == 200, response.status
    print("ingestion accepted:", service)

    def query(method, data):
        req = urllib.request.Request(
            args.url.rstrip("/") + "/querier.v1.QuerierService/" + method,
            data=json.dumps(data).encode(), headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=30) as response:
            return json.load(response)

    # Profiles carry their capture timestamp. A narrow recent window avoids
    # conflating captures; generate the fixture immediately before this check.
    window = {"start": (now - 1800) * 1000, "end": (now + 60) * 1000}
    types = query("ProfileTypes", window)["profileTypes"]
    selector = "{" + ",".join(
        key + "=" + json.dumps(value)
        for key, value in {"service_name": service, **report["labels"]}.items()) + "}"
    checked = []
    for category in ("reachable", "exclusive", "shared"):
        for suffix, measure in (("space", "bytes"), ("objects", "objects")):
            sample = category + "_" + suffix
            matches = [p for p in types if p["sampleType"] == sample]
            assert matches, ("missing custom type", sample, types)
            result = query("SelectMergeStacktraces", {
                **window, "labelSelector": selector,
                "profileTypeID": matches[0]["ID"], "maxNodes": 100,
            })
            flamegraph = result.get("flamegraph", {})
            expected = metrics[category][measure]
            assert int(flamegraph.get("total", 0)) == expected, (sample, expected, result)
            if expected:
                names = flamegraph.get("names", [])
                assert any("[bubblepprof summary]" in n for n in names), names
                assert all(n == "total" or n.startswith("[bubblepprof summary]") for n in names), names
            checked.append({"sample_type": sample, "total": expected})
    print(json.dumps({"service": service, "selector": selector, "verified": checked}, indent=2))


if __name__ == "__main__":
    try:
        main()
    except urllib.error.HTTPError as error:
        raise SystemExit(f"HTTP {error.code}: {error.read().decode(errors='replace')}")
