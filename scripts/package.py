#!/usr/bin/env python3
"""Build a Linux amd64 release without running the inherited test suite."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("version")
    args = parser.parse_args()
    if not re.fullmatch(r"\d{4}\.\d{2}\.\d{2}-\d{6}", args.version):
        parser.error("版本格式为 YYYY.MM.DD-HHMMSS")
    output = ROOT / "dist" / args.version
    stage = output / "bundle"
    (stage / "bin").mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
    for directory, binary in [("companion", "go-emby-scraper"), ("gateway", "scraper-gateway")]:
        flags = "-s -w" + (" -X main.releaseVersion=" + args.version if directory == "gateway" else "")
        subprocess.run(["go", "build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-ldflags=" + flags, "-o", str(stage / "bin" / binary), "."], cwd=ROOT / directory, env=env, check=True)
    original = ROOT / "runtime" / "go-emby-linux-amd64"
    provenance = json.loads((ROOT / "runtime" / "provenance.json").read_text(encoding="utf-8"))
    if hashlib.sha256(original.read_bytes()).hexdigest() != provenance["SHA256"]:
        raise RuntimeError("原程序运行文件校验失败")
    shutil.copy2(original, stage / "bin" / "go-emby")
    shutil.copytree(ROOT / "frontend", stage / "frontend", dirs_exist_ok=True)
    shutil.copy2(ROOT / "Dockerfile.runtime", stage / "Dockerfile")
    shutil.copy2(ROOT / "runtime" / "provenance.json", stage / "runtime-provenance.json")
    (stage / "VERSION").write_text(args.version + "\n", encoding="utf-8")
    asset = "ai-emby-linux-amd64.tar.gz"
    archive = output / asset
    with tarfile.open(archive, "w:gz") as bundle:
        for path in sorted(stage.rglob("*")):
            if path.is_file():
                info = bundle.gettarinfo(str(path), arcname=path.relative_to(stage).as_posix())
                info.uid = info.gid = 0
                info.uname = info.gname = "root"
                info.mode = 0o755 if info.name.startswith("bin/") else 0o644
                with path.open("rb") as source:
                    bundle.addfile(info, source)
    manifest = {"Repository": "LLL198/ai-emby", "Version": args.version, "Architecture": "amd64", "Asset": asset, "SHA256": hashlib.sha256(archive.read_bytes()).hexdigest()}
    (output / "update-linux-amd64.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    (output / "SHA256SUMS").write_text(manifest["SHA256"] + "  " + asset + "\n", encoding="utf-8")
    print("Release artifacts: " + str(output))


if __name__ == "__main__":
    main()
