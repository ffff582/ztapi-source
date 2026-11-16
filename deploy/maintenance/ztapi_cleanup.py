#!/usr/bin/env python3
"""Bounded ZTAPI release cleanup; default is a JSON dry run, never a prune."""
import argparse
import datetime
try:
    import fcntl
except ImportError:
    fcntl = None
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import time

RELEASES = Path("/opt/ztapi/releases")
TEMP = Path("/tmp")
ALWAYS_KEEP = {"claude-spend-20261102-r1", "commercial-20261031-r6", "hosted-search-20261101-r23-b6", "referral-activation-20261108", "referral-activation-20261108-r2", "support-20261102-r1"}


def docker(*args):
    result = subprocess.run(["docker", *args], capture_output=True, timeout=60, check=True)
    return result.stdout.decode()


def safe_target(path, roots):
    path = Path(os.path.abspath(path))
    return not path.is_symlink() and path.resolve() == path and any(path != root.resolve() and path.is_relative_to(root.resolve()) for root in roots)


def mounted_paths(containers):
    paths = []
    for container in containers:
        for mount in container.get("Mounts", []):
            paths.append(Path(mount["Source"]).resolve())
        labels = container.get("Config", {}).get("Labels") or {}
        for name in labels.get("com.docker.compose.project.config_files", "").split(","):
            if name:
                paths.append(Path(name).resolve())
    return paths


def overlaps(path, protected):
    return any(path == other or path.is_relative_to(other) or other.is_relative_to(path) for other in protected)


def has_credentials(path):
    def sensitive(value):
        if isinstance(value, dict):
            return any(re.search(r"(?i)(api.?key|password|secret|credential|^token$|dsn)", key) or sensitive(item) for key, item in value.items())
        return isinstance(value, list) and any(sensitive(item) for item in value)
    for file in path.rglob("*"):
        if not file.is_file():
            continue
        if file.name == ".env" or file.suffix.lower() in {".env", ".key", ".pem"} or "secret" in file.name.lower() or file.name in {"auth.json", "authorized_keys", "id_ed25519", "id_rsa"}:
            return True
        if file.suffix.lower() == ".json":
            if file.stat().st_size > 1 << 20:
                return True
            try:
                if sensitive(json.loads(file.read_bytes())):
                    return True
            except (ValueError, OSError):
                return True
    return False


def release_candidates(root, containers):
    root = root.resolve()
    folders = sorted((p for p in root.iterdir() if p.is_dir() and not p.is_symlink()), key=lambda p: p.stat().st_mtime, reverse=True)
    mounted = mounted_paths(containers)
    kept = set(folders[:5]) | {p for p in folders if p.name in ALWAYS_KEEP or overlaps(p, mounted) or (p / ".deploy-active").exists()}
    paths = []
    for folder in folders:
        if folder in kept:
            continue
        for target in list(folder.glob("*.test")) + [folder / "build-cache", folder / "build-out"]:
            if target.exists() and safe_target(target, [root]) and not overlaps(target, mounted) and not (target.is_dir() and has_credentials(target)):
                paths.append(target)
    return sorted(paths)


def temporary_candidates(root, containers, now):
    protected = mounted_paths(containers)
    return sorted(path for path in root.glob("ztapi-*") if path.is_dir() and safe_target(path, [root]) and now - path.stat().st_mtime > 72 * 3600 and not overlaps(path, protected) and not has_credentials(path))


def stopped_candidates(containers, now):
    rollback = sorted((c for c in containers if c["Name"].startswith("/ztapi-server-before-") and not c["State"]["Running"]), key=lambda c: c["State"].get("FinishedAt", ""), reverse=True)[:2]
    protected = {c["Name"] for c in rollback}
    result = []
    for container in containers:
        name, state = container["Name"], container["State"]
        if state["Running"] or name in protected or not name.startswith("/ztapi-") or not re.search(r"(?:^|[-_])(candidate|before)(?:[-_]|$)", name):
            continue
        stamp = state.get("FinishedAt", "")
        try:
            finished = datetime.datetime.fromisoformat(re.sub(r"(\.\d{6})\d+", r"\1", stamp).replace("Z", "+00:00")).timestamp()
        except ValueError:
            continue
        if now - finished > 72 * 3600:
            result.append(name.lstrip("/"))
    return sorted(result)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--post-release")
    args = parser.parse_args()
    if fcntl is None:
        raise SystemExit("Linux host required")
    if args.apply and os.geteuid() != 0:
        raise SystemExit("root required")
    with open("/run/ztapi-maintenance.lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        ids = docker("ps", "-aq").split()
        containers = json.loads(docker("inspect", *ids)) if ids else []
        paths = release_candidates(RELEASES, containers) + temporary_candidates(TEMP, containers, time.time())
        if args.post_release:
            current = RELEASES / args.post_release
            if current.name != args.post_release or not safe_target(current, [RELEASES]) or not (current / "production-verified.json").is_file():
                raise SystemExit("verified release directory required")
            for path in list(current.glob("*.test")) + [current / "build-cache", current / "build-out"]:
                if path.exists() and safe_target(path, [RELEASES]) and not overlaps(path, mounted_paths(containers)) and not (path.is_dir() and has_credentials(path)):
                    paths.append(path)
        stopped = stopped_candidates(containers, time.time())
        result = {"apply": args.apply, "paths": [str(path) for path in paths], "stopped_candidates": stopped, "deleted_images": [], "preserves_credentials_and_evidence": True}
        if args.apply:
            for path in paths:
                if not safe_target(path, [RELEASES, TEMP]):
                    raise SystemExit("unsafe cleanup path")
                if path.is_dir():
                    shutil.rmtree(path)
                else:
                    path.unlink()
            for name in stopped:
                docker("rm", name)
            # Only unused ZTAPI main images; Docker refuses shared/in-use parents.
            used = {c["Image"] for c in containers if c["Name"].lstrip("/") not in stopped}
            for dockerfile in RELEASES.glob("*/Dockerfile*"):
                if safe_target(dockerfile, [RELEASES]) and dockerfile.is_file():
                    used.update(re.findall(r"(?im)^FROM\s+(sha256:[a-f0-9]{64})\b", dockerfile.read_text(errors="replace")))
            image_ids = docker("images", "ztapi-main", "-q", "--no-trunc").split()
            for image in dict.fromkeys(image_ids):
                if image in used:
                    continue
                try:
                    docker("image", "rm", image)
                    result["deleted_images"].append(image)
                except subprocess.CalledProcessError:
                    pass
        print(json.dumps(result, separators=(",", ":")))


if __name__ == "__main__":
    main()
