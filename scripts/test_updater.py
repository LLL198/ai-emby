import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("updater", Path(__file__).with_name("updater.py"))
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


class CleanupTests(unittest.TestCase):
    def test_cleanup_keeps_one_backup_and_logs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old, current = root / "2026.10.01-120000-1791000001", root / "2026.10.06-120000-1791000002"
            unrelated = root / "manual-2026.10.01-120000"
            for p in (old, current, unrelated):
                p.mkdir()
                (p / "compose.before.yaml").write_text("compose")
                (p / "update.log").write_text("log")
                (p / "ai-emby-linux-amd64.tar.gz").write_bytes(b"archive")
                (p / "bundle").mkdir()
                (p / "bundle" / "file").write_bytes(b"binary")
                for name in updater.BACKUP_FILES:
                    (p / name).write_bytes(b"backup")
            updater.cleanup_update(current, current)
            self.assertTrue(updater.complete_backup(current))
            self.assertFalse(updater.complete_backup(old))
            for p in (old, current):
                self.assertFalse((p / "bundle").exists())
                self.assertFalse((p / "ai-emby-linux-amd64.tar.gz").exists())
                self.assertTrue((p / "update.log").exists())
            self.assertTrue((unrelated / "bundle").exists())
            self.assertTrue(updater.complete_backup(unrelated))

    def test_incomplete_backup_is_not_reusable(self):
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory)
            (p / updater.BACKUP_FILES[0]).write_bytes(b"valid")
            (p / updater.BACKUP_FILES[1]).touch()
            self.assertFalse(updater.complete_backup(p))

    def test_cleanup_rejects_escape_and_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            inside = root / "updates"
            inside.mkdir()
            external = root / "outside"
            external.mkdir()
            (external / "important").write_bytes(b"keep")
            with self.assertRaises(ValueError):
                updater.remove_update_file(inside, external)
            link = inside / "bundle"
            link.symlink_to(external, target_is_directory=True)
            with self.assertRaises(ValueError):
                updater.remove_update_file(inside, link)
            self.assertTrue((external / "important").exists())

    def test_backup_tasks_run_concurrently(self):
        barrier = threading.Barrier(2)
        def backup(command, stdout, **kwargs):
            barrier.wait(timeout=3)
            stdout.write(b"snapshot")
        with tempfile.TemporaryDirectory() as directory, patch.object(updater.subprocess, "run", side_effect=backup):
            p = Path(directory)
            with (p / "log").open("w") as log:
                updater.backup_data(p, "db", "app", log)
            self.assertTrue(updater.complete_backup(p))


class UpdateTests(unittest.TestCase):
    def exercise(self, failure=False, cleanup_failure=False):
        with tempfile.TemporaryDirectory() as directory:
            project = Path(directory)
            (project / "update-control").mkdir()
            old = project / "app-backups/updates/2026.10.01-120000-1791000001"
            old.mkdir(parents=True)
            (old / "compose.before.yaml").write_text("before")
            for name in updater.BACKUP_FILES:
                (old / name).write_bytes(b"old-backup")
            compose = project / "compose.yaml"
            compose.write_text("services:\n  app:\n    image: ghcr.io/lll198/ai-emby:old\n")
            version = "2026.10.06-120000"
            request = {"Repository": updater.REPOSITORY, "Version": version,
                       "ManifestURL": f"https://github.com/{updater.REPOSITORY}/releases/download/{version}/update-linux-amd64.json"}
            commands = []
            def download(url, target, proxy):
                if target.name == "manifest.json":
                    target.write_text(json.dumps({"Repository": updater.REPOSITORY, "Version": version,
                        "Architecture": "amd64", "Asset": "ai-emby-linux-amd64.tar.gz",
                        "SHA256": hashlib.sha256(b"archive").hexdigest()}))
                else:
                    target.write_bytes(b"archive")
            def extract(archive, destination):
                (destination / "VERSION").write_text(version)
            def output(command, **kwargs):
                if command[1] == "inspect":
                    return "ghcr.io/lll198/ai-emby:old"
                if command[1] == "exec":
                    return version
                return "container"
            def backup(work, *args):
                for name in updater.BACKUP_FILES:
                    (work / name).write_bytes(b"new-backup")
            with patch.object(updater,"download",side_effect=download), \
                 patch.object(updater,"extract_bundle",side_effect=extract), \
                 patch.object(updater.subprocess,"check_output",side_effect=output), \
                 patch.object(updater,"require_persistent_storage"), \
                 patch.object(updater,"backup_data",side_effect=backup), \
                 patch.object(updater,"run",side_effect=lambda c,*a:commands.append(c)), \
                 patch.object(updater,"wait_healthy",side_effect=[RuntimeError("unhealthy"),None] if failure else None), \
                 patch.object(updater,"cleanup_update",side_effect=OSError("disk error") if cleanup_failure else updater.cleanup_update):
                updater.perform_update(SimpleNamespace(project=project,service="app",mirror_compose=None),request)
            state=json.loads((project / "update-control/status.json").read_text())
            current=next(p for p in old.parent.iterdir() if p!=old)
            if failure:
                self.assertEqual(state["State"],"failed")
                self.assertIn("ai-emby:old",compose.read_text())
                self.assertTrue(updater.complete_backup(old))
                self.assertTrue((current / "bundle").exists())
            else:
                self.assertEqual(state["State"],"succeeded")
                self.assertIn(version,compose.read_text())
                self.assertTrue(updater.complete_backup(current))
                if cleanup_failure:
                    self.assertTrue((current / "cleanup.log").exists())
                    self.assertTrue(updater.complete_backup(old))
                    self.assertEqual(sum(c[:3]==["docker","compose","up"] for c in commands),1)
                else:
                    self.assertFalse(updater.complete_backup(old))
                    self.assertFalse((current / "bundle").exists())
                    self.assertFalse((current / "ai-emby-linux-amd64.tar.gz").exists())

    def test_success_rotates_backup_and_removes_package(self):
        self.exercise()

    def test_failed_update_preserves_previous_backup_and_package(self):
        self.exercise(failure=True)

    def test_cleanup_error_does_not_rollback_healthy_update(self):
        self.exercise(cleanup_failure=True)


if __name__ == "__main__":
    unittest.main()
