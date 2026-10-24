"""Copy every third-party server image into the mirror registry, by digest.

The chart is the only description of these images: servers.<x>.image in
deploy/chart/values.yaml gives the mirror name, tag and digest, and
deploy/chart/ci/upstream-values.yaml gives each upstream repository.

  hack/mirror.py --check                 every server has a digest and an upstream
  hack/mirror.py --dest public.ecr.aws/<alias>/kubeshark/mirror [--dry-run]
"""

import argparse
import pathlib
import subprocess
import sys

import yaml

root = pathlib.Path(__file__).resolve().parent.parent
values = yaml.safe_load((root / "deploy/chart/values.yaml").read_text())["servers"]
upstream = yaml.safe_load((root / "deploy/chart/ci/upstream-values.yaml").read_text())["servers"]


def images():
    problems, out = [], []
    for key, server in values.items():
        img = server["image"]
        repo = upstream.get(key, {}).get("image", {}).get("repository")
        if not repo:
            problems.append(f"{key}: no upstream repository in ci/upstream-values.yaml")
        if not img.get("digest", "").startswith("sha256:"):
            problems.append(f"{key}: image.digest is not pinned")
        out.append((key, repo, img["name"], str(img["tag"]), img.get("digest", "")))
    extra = set(upstream) - set(values)
    problems += [f"{k}: in ci/upstream-values.yaml but not a server" for k in sorted(extra)]
    return out, problems


def digest(ref):
    r = subprocess.run(["crane", "digest", ref], capture_output=True, text=True)
    return r.stdout.strip() if r.returncode == 0 else ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--dest")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    imgs, problems = images()
    for p in problems:
        print(f"FAIL {p}")
    if problems:
        return 1
    if args.check:
        print(f"ok   {len(imgs)} server images pinned with upstreams")
        return 0
    if not args.dest:
        ap.error("--dest is required unless --check")

    for key, repo, name, tag, dig in imgs:
        src, dst = f"{repo}@{dig}", f"{args.dest}/{name}:{tag}"
        if digest(dst) == dig:
            print(f"ok   {key}: {dst} already at {dig}")
            continue
        print(f"copy {key}: {src} -> {dst}")
        if not args.dry_run:
            subprocess.run(["crane", "copy", src, dst], check=True)
            if digest(dst) != dig:
                print(f"FAIL {key}: {dst} does not resolve to {dig} after copy")
                return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
