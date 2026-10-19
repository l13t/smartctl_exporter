#!/usr/bin/env python3
"""Regenerate testdata/golden/python.prom from the original Python exporter.

Downloads smartprom.py + megaraid.py from matusnovak/prometheus-smartctl at a
pinned commit, points them at testdata/fake-smartctl, runs one discovery and
one collection, and writes the smartprom_* series in Prometheus text format.

Requires: pip install prometheus-client==0.19.0  (the version upstream pinned)
Usage:    python3 scripts/gen_golden.py
"""
import os
import sys
import tempfile
import urllib.request

UPSTREAM_SHA = "e3e2f6f8121d53d79bf41dc9d6597a9d05284eee"  # v2.3.0 + later fixes, master 2026-10
UPSTREAM_URL = "https://raw.githubusercontent.com/matusnovak/prometheus-smartctl/{sha}/{name}"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FAKE = os.path.join(ROOT, "testdata", "fake-smartctl")
OUT = os.path.join(ROOT, "testdata", "golden", "python.prom")


def main() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        src = os.path.join(tmp, "src")
        bin_dir = os.path.join(tmp, "bin")
        os.makedirs(src)
        os.makedirs(bin_dir)
        for name in ("smartprom.py", "megaraid.py"):
            url = UPSTREAM_URL.format(sha=UPSTREAM_SHA, name=name)
            with urllib.request.urlopen(url) as resp, open(os.path.join(src, name), "wb") as f:
                f.write(resp.read())
        os.symlink(FAKE, os.path.join(bin_dir, "smartctl"))
        os.environ["PATH"] = bin_dir + os.pathsep + os.environ["PATH"]
        os.environ["FAKE_SMARTCTL_DIR"] = os.path.join(ROOT, "testdata", "scenario")

        sys.path.insert(0, src)
        import prometheus_client
        import smartprom

        smartprom.DRIVES = smartprom.get_drives()
        smartprom.collect()
        text = prometheus_client.generate_latest(prometheus_client.REGISTRY).decode()

    lines = [l for l in text.splitlines() if l.startswith("smartprom_") or l.startswith("# HELP smartprom_") or l.startswith("# TYPE smartprom_")]
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "w") as f:
        f.write("\n".join(lines) + "\n")
    print(f"wrote {OUT}: {sum(1 for l in lines if not l.startswith('#'))} series")


if __name__ == "__main__":
    main()
