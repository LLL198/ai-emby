#!/usr/bin/env python3
"""Build a Linux amd64 release bundle."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
ENGINE_URL = "https://github.com/OpenListTeam/OpenList/releases/download/v4.2.6/openlist-linux-amd64-lite.tar.gz"
ENGINE_SHA256 = "a3bf640adae8b72b9b63deb76111eae21222f964c184193f80a18924b8037437"


def package_cloud_engine(output, stage):
    archive = output / "cloud-engine.tar.gz"
    for attempt in range(3):
        try:
            request = urllib.request.Request(ENGINE_URL, headers={"User-Agent": "AI-Emby-release"})
            with urllib.request.urlopen(request, timeout=60) as response, archive.open("wb") as target:
                if not response.geturl().startswith("https://"):
                    raise RuntimeError("网盘引擎下载地址不安全")
                total = 0
                while chunk := response.read(1024 * 1024):
                    total += len(chunk)
                    if total > 64 * 1024 * 1024:
                        raise RuntimeError("网盘引擎归档超出大小限制")
                    target.write(chunk)
            break
        except OSError:
            if attempt == 2:
                raise
            time.sleep(2)
    with archive.open("rb") as source:
        if hashlib.file_digest(source, "sha256").hexdigest() != ENGINE_SHA256:
            raise RuntimeError("网盘引擎归档校验失败")
    # Older installed updaters accept frontend/* but reject additional bin/* entries.
    # The runtime Dockerfile installs these files and removes them from the web root.
    distribution = stage / "frontend" / ".release"
    distribution.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, "r:gz") as bundle:
        members = [member for member in bundle if Path(member.name).name == "openlist"]
        if len(members) != 1 or not members[0].isfile() or members[0].size > 128 * 1024 * 1024:
            raise RuntimeError("网盘引擎运行文件无效")
        with bundle.extractfile(members[0]) as source, (distribution / "ai-emby-cloud-engine").open("wb") as target:
            shutil.copyfileobj(source, target)
    shutil.copytree(ROOT / "third_party", distribution / "third_party")
    shutil.copytree(ROOT / "disc-reader", distribution / "disc-reader")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("version")
    args = parser.parse_args()
    if not re.fullmatch(r"\d{4}\.\d{2}\.\d{2}-\d{6}", args.version):
        parser.error("版本格式为 YYYY.MM.DD-HHMMSS")
    output = ROOT / "dist" / args.version
    stage = output / "bundle"
    if stage.exists():
        raise RuntimeError("打包目录已存在，请使用新的版本号")
    (stage / "bin").mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
    for directory, binary in [("companion", "go-emby-scraper"), ("gateway", "scraper-gateway")]:
        flags = "-s -w" + (" -X main.releaseVersion=" + args.version if directory == "gateway" else "")
        subprocess.run(["go", "build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-ldflags=" + flags, "-o", str(stage / "bin" / binary), "."], cwd=ROOT / directory, env=env, check=True)
    # Preserve archive paths supported by installed updaters; both services use source builds.
    shutil.copy2(stage / "bin" / "go-emby-scraper", stage / "bin" / "go-emby")
    shutil.copytree(ROOT / "frontend", stage / "frontend", dirs_exist_ok=True)
    package_cloud_engine(output, stage)
    shutil.copy2(ROOT / "Dockerfile.runtime", stage / "Dockerfile")
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    runtime_manifest = {"Architecture": "linux/amd64", "Version": args.version,
                        "CoreImplementation": "source", "SourceRevision": revision,
                        "Compiler": subprocess.check_output(["go", "version"], text=True).strip(),
                        "SHA256": hashlib.sha256((stage / "bin" / "go-emby").read_bytes()).hexdigest(),
                        "Executables": {name: hashlib.sha256((stage / "bin" / name).read_bytes()).hexdigest()
                                        for name in ["go-emby", "go-emby-scraper", "scraper-gateway"]}}
    (stage / "runtime-provenance.json").write_text(json.dumps(runtime_manifest, indent=2) + "\n", encoding="utf-8")
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
