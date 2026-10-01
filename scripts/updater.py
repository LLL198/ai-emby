#!/usr/bin/env python3
"""Host worker for the authenticated AI Emby update queue. Linux + Docker Compose."""
import argparse
import concurrent.futures
import datetime
import fcntl
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

REPOSITORY = "LLL198/ai-emby"
TAG = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$")


def timestamp():
    return datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")


def atomic_json(path, value):
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False), encoding="utf-8")
    temporary.chmod(0o600)
    temporary.replace(path)


def run(args, cwd, log):
    subprocess.run(args, cwd=cwd, stdout=log, stderr=log, check=True, timeout=900)


def download(url, target, proxy):
    # Proxy credentials go through stdin, not process arguments or logs.
    config = ""
    if proxy:
        if any(c in proxy for c in "\r\n\x00"):
            raise ValueError("更新代理无效")
        config = 'proxy = "' + proxy.replace("\\", "\\\\").replace('"', '\\"') + '"\n'
    result = subprocess.run([
        "curl", "--config", "-", "--proto", "=https", "--proto-redir", "=https",
        "--fail", "--location", "--silent", "--show-error", "--retry", "2",
        "--connect-timeout", "20", "--max-time", "600", "--output", str(target), url,
    ], input=config, text=True, capture_output=True, timeout=650)
    if result.returncode:
        raise RuntimeError("下载新仓库更新包失败，请检查网络或更新代理")


def extract_bundle(archive, destination):
    required = {"Dockerfile", "VERSION", "bin/go-emby", "bin/go-emby-scraper", "bin/scraper-gateway"}
    allowed = required | {"manifest.json", "runtime-provenance.json"}
    seen = set()
    total = 0
    with tarfile.open(archive, "r:gz") as bundle:
        for member in bundle:
            name = member.name
            pieces = name.split("/")
            if member.isdir():
                continue
            if not member.isfile() or name.startswith("/") or any(p in ("", ".", "..") for p in pieces):
                raise ValueError("更新包包含无效文件")
            if name not in allowed and not name.startswith("frontend/"):
                raise ValueError("更新包包含未允许的文件")
            if name in seen or member.size > 128 * 1024 * 1024:
                raise ValueError("更新包文件超出限制")
            total += member.size
            if total > 256 * 1024 * 1024:
                raise ValueError("更新包超出大小限制")
            target = destination / name
            target.parent.mkdir(parents=True, exist_ok=True)
            source = bundle.extractfile(member)
            with target.open("wb") as output:
                shutil.copyfileobj(source, output)
            target.chmod(0o755 if name.startswith("bin/") else 0o644)
            seen.add(name)
    if not required <= seen:
        raise ValueError("更新包不完整")


def healthy(project, service):
    container = subprocess.check_output(["docker", "compose", "ps", "-q", service], cwd=project, text=True).strip()
    if not container:
        return False
    state = json.loads(subprocess.check_output(["docker", "inspect", container], text=True))[0]
    if state["State"].get("Health", {}).get("Status") != "healthy":
        return False
    result = subprocess.run(["docker", "exec", container, "wget", "-qO-", "http://127.0.0.1:8097/health"], capture_output=True, text=True, timeout=10)
    if result.returncode:
        return False
    try:
        body = json.loads(result.stdout)
        return body.get("status") == "ok" and body.get("scanConcurrency") and body.get("scraperConcurrency")
    except ValueError:
        return False


def wait_healthy(project, service):
    for _ in range(60):
        try:
            if healthy(project, service):
                return
        except (OSError, ValueError, subprocess.SubprocessError):
            pass
        time.sleep(2)
    raise RuntimeError("新容器未恢复健康状态")


def application_service(project):
    configuration = json.loads(subprocess.check_output(
        ["docker", "compose", "config", "--format", "json"],
        cwd=project, text=True, timeout=30))
    repository = "ghcr.io/lll198/ai-emby"
    candidates = []
    for name, settings in configuration.get("services", {}).items():
        image = settings.get("image", "")
        if image == repository or image.startswith((repository + ":", repository + "@")):
            candidates.append(name)
    if len(candidates) != 1:
        raise ValueError("无法识别应用服务，请使用 --service 指定 Compose 服务名")
    return candidates[0]


def perform_update(options, request):
    control = options.project / "update-control"
    version = request.get("Version", "")
    work = options.project / "app-backups" / "updates" / (version + "-" + str(int(time.time())))
    switched = False
    previous = []

    def status(state, message):
        atomic_json(control / "status.json", {"State": state, "Message": message, "TargetVersion": version, "Updated": timestamp()})

    try:
        if request.get("Repository") != REPOSITORY or not TAG.fullmatch(version):
            raise ValueError("更新任务仓库或版本无效")
        url = f"https://github.com/{REPOSITORY}/releases/download/{version}/update-linux-amd64.json"
        if request.get("ManifestURL") != url:
            raise ValueError("更新清单地址无效")
        work.mkdir(parents=True, mode=0o700)
        status("downloading", "正在下载更新包")
        download(url, work / "manifest.json", request.get("Proxy", ""))
        manifest = json.loads((work / "manifest.json").read_text(encoding="utf-8"))
        asset = "ai-emby-linux-amd64.tar.gz"
        if manifest.get("Repository") != REPOSITORY or manifest.get("Version") != version or manifest.get("Architecture") != "amd64" or manifest.get("Asset") != asset or not re.fullmatch(r"[a-f0-9]{64}", manifest.get("SHA256", "")):
            raise ValueError("更新清单校验失败")
        archive = work / asset
        download(f"https://github.com/{REPOSITORY}/releases/download/{version}/{asset}", archive, request.get("Proxy", ""))
        with archive.open("rb") as source:
            digest = hashlib.file_digest(source, "sha256").hexdigest()
        if digest != manifest["SHA256"]:
            raise ValueError("更新包 SHA256 校验失败")
        bundle = work / "bundle"
        bundle.mkdir()
        extract_bundle(archive, bundle)
        if (bundle / "VERSION").read_text().strip() != version:
            raise ValueError("更新包版本不一致")
        with (work / "update.log").open("w", encoding="utf-8") as log:
            container = subprocess.check_output(["docker", "compose", "ps", "-q", options.service], cwd=options.project, text=True).strip()
            old_image = subprocess.check_output(["docker", "inspect", container, "--format", "{{.Config.Image}}"], text=True).strip()
            new_image = "ghcr.io/lll198/ai-emby:" + version
            status("building", "正在构建新版本镜像")
            run(["docker", "build", "--no-cache", "--build-arg", "RUNTIME_BASE=" + old_image, "-t", new_image, str(bundle)], options.project, log)
            files = [options.project / "compose.yaml"]
            if options.mirror_compose:
                files.append(options.mirror_compose)
            for path in files:
                content = path.read_text(encoding="utf-8")
                image = re.escape(old_image)
                image += r"|ghcr\.io/lll198/ai-emby:\$\{IMAGE_TAG:-latest\}"
                pattern = r"(?m)^([ \t]*image:[ \t]*)(?:" + image + r")[ \t]*$"
                changed, count = re.subn(pattern, lambda match: match.group(1) + new_image, content)
                if count != 1:
                    raise ValueError("无法定位 Compose 中的应用镜像")
                previous.append((path, content, changed))
            (work / "compose.before.yaml").write_text(previous[0][1], encoding="utf-8")
            postgres = subprocess.check_output(["docker", "compose", "ps", "-q", "postgres"], cwd=options.project, text=True).strip()
            if postgres:
                status("backup", "正在备份数据库")
                with (work / "database.before.dump").open("wb") as output:
                    subprocess.run(["docker", "exec", postgres, "pg_dump", "-U", "emby", "-d", "emby", "-Fc", "--no-owner"], stdout=output, stderr=log, check=True, timeout=300)
            status("restarting", "正在切换版本")
            switched = True
            for path, _, changed in previous:
                path.write_text(changed, encoding="utf-8")
            run(["docker", "compose", "up", "-d", "--no-deps", "--no-build", options.service], options.project, log)
            wait_healthy(options.project, options.service)
            active = subprocess.check_output(["docker", "compose", "ps", "-q", options.service], cwd=options.project, text=True).strip()
            installed = subprocess.check_output(["docker", "exec", active, "cat", "/app/VERSION"], text=True).strip()
            if installed != version:
                raise ValueError("运行版本校验失败")
        status("succeeded", "更新完成")
    except Exception as error:
        message = str(error) if isinstance(error, (ValueError, RuntimeError)) else "更新执行失败，请查看宿主机 app-backups/updates 中的日志"
        if switched:
            try:
                for path, content, _ in previous:
                    path.write_text(content, encoding="utf-8")
                with (work / "rollback.log").open("w") as log:
                    run(["docker", "compose", "up", "-d", "--no-deps", "--no-build", options.service], options.project, log)
                wait_healthy(options.project, options.service)
                message += "；已恢复旧版本"
            except Exception:
                message += "；自动恢复未完成，请检查宿主机"
        status("failed", message)
    finally:
        (control / "processing.json").unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--project", type=Path, required=True)
    parser.add_argument("--service", help="Compose application service; detected from the image by default")
    parser.add_argument("--mirror-compose", type=Path)
    options = parser.parse_args()
    options.project = options.project.resolve()
    if not options.service:
        try:
            options.service = application_service(options.project)
        except (OSError, ValueError, subprocess.SubprocessError):
            parser.error("无法识别应用服务，请使用 --service 指定 Compose 服务名")
    control = options.project / "update-control"
    control.mkdir(mode=0o700, exist_ok=True)
    with (control / "worker.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if (control / "processing.json").exists():
            atomic_json(control / "status.json", {"State": "failed", "Message": "更新服务曾中断，请确认运行状态后重新更新", "Updated": timestamp()})
            (control / "processing.json").unlink()
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            task = None
            while True:
                atomic_json(control / "ready.json", {"Repository": REPOSITORY, "Time": timestamp()})
                if (task is None or task.done()) and (control / "request.json").exists():
                    (control / "request.json").replace(control / "processing.json")
                    try:
                        request = json.loads((control / "processing.json").read_text(encoding="utf-8"))
                    except ValueError:
                        request = {}
                    task = pool.submit(perform_update, options, request)
                time.sleep(2)


if __name__ == "__main__":
    main()
