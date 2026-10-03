#!/usr/bin/env python3
"""Move an existing single-file Compose deployment to persistent Docker volumes."""
import argparse
import datetime
import json
import os
from pathlib import Path
import re
import subprocess
import time


def migrate(project):
    os.umask(0o077)
    project = project.resolve()
    compose_file = project / 'compose.yaml'
    original = compose_file.read_text(encoding='utf-8')
    timestamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    backup = project / 'app-backups' / 'storage' / timestamp
    backup.mkdir(parents=True, exist_ok=False)

    def run(args, **kwargs):
        result = subprocess.run(args, capture_output=True, cwd=project, timeout=600, **kwargs)
        if result.returncode:
            (backup / 'error.log').write_bytes(result.stderr)
            raise RuntimeError('操作失败；详情保存在 ' + str(backup / 'error.log'))
        return result.stdout

    def compose(*args):
        return run(['docker', 'compose', '-f', str(compose_file), *args])

    configuration = json.loads(compose('config', '--format', 'json'))
    candidates = [name for name, settings in configuration['services'].items()
                  if settings.get('image', '').startswith('ghcr.io/lll198/ai-emby')]
    if len(candidates) != 1 or 'postgres' not in configuration['services']:
        raise ValueError('无法识别应用和数据库服务，请使用原部署的 compose.yaml')
    service = candidates[0]
    app = compose('ps', '-q', service).decode().strip()
    database = compose('ps', '-q', 'postgres').decode().strip()
    if not app or not database:
        raise ValueError('迁移前需要原应用和数据库正常运行；数据已丢失时请先恢复备份')
    database_info = json.loads(run(['docker', 'inspect', database]))[0]
    app_info = json.loads(run(['docker', 'inspect', app]))[0]
    if any(m['Destination'] == '/var/lib/postgresql/data' and m['Type'] == 'volume' for m in database_info['Mounts']) and any(m['Destination'] == '/app/data' and m['Type'] == 'volume' for m in app_info['Mounts']):
        print('数据库与应用已经使用 Docker 持久卷，无需迁移。')
        return
    if re.search(r'^volumes:', original, re.M):
        raise ValueError('已有自定义卷配置，请先保留原配置并单独迁移')
    prefix = configuration['name'] + '-persistent-' + timestamp.lower()
    database_volume, app_volume = prefix + '-db', prefix + '-app'
    changed = original
    for destination, replacement in [('/var/lib/postgresql/data', 'database-persistent'), ('/app/data', 'application-persistent')]:
        pattern = r'(?m)^([ \t]*-[ \t]*)[^\r\n]+:' + re.escape(destination) + r'(?::rw)?[ \t]*$'
        changed, number = re.subn(pattern, lambda match: match[1] + replacement + ':' + destination, changed)
        if number != 1:
            raise ValueError('无法识别数据挂载，请保留原 Compose 文件并单独迁移')
    changed += '\nvolumes:\n  database-persistent:\n    external: true\n    name: ' + database_volume + '\n  application-persistent:\n    external: true\n    name: ' + app_volume + '\n'
    (backup / 'compose.before.yaml').write_text(original, encoding='utf-8')
    (backup / 'compose.after.yaml').write_text(changed, encoding='utf-8')
    state = {'database_volume': database_volume, 'app_volume': app_volume, 'phase': 'backup'}

    def save(phase):
        state['phase'] = phase
        (backup / 'migration.json').write_text(json.dumps(state, indent=2), encoding='utf-8')

    def wait(container):
        for attempt in range(120):
            result = subprocess.run(['docker', 'exec', container, 'pg_isready', '-U', 'emby', '-d', 'emby'], capture_output=True)
            if result.returncode == 0:
                return
            time.sleep(1)
        raise RuntimeError('新数据库未就绪；备份及卷已保留在 ' + str(backup))

    print('正在停止应用并备份，原数据库暂时保持运行。', flush=True)
    stopped, switched = False, False
    try:
        compose('stop', service)
        stopped = True
        save('backup')
        # A tmpfs database loses its files on stop, so dump it before stopping.
        dump = backup / 'database.dump'
        with dump.open('wb') as output:
            subprocess.run(['docker', 'exec', database, 'pg_dump', '-U', 'emby', '-d', 'emby', '-Fc', '--no-owner'], stdout=output, stderr=subprocess.PIPE, check=True, timeout=600)
        archive = backup / 'application.tar'
        with archive.open('wb') as output:
            subprocess.run(['docker', 'cp', app + ':/app/data/.', '-'], stdout=output, stderr=subprocess.PIPE, check=True, timeout=600)
        for volume in (database_volume, app_volume):
            run(['docker', 'volume', 'create', volume])
        stage = prefix + '-stage'
        env = os.environ.copy()
        private_env = dict(value.split('=', 1) for value in database_info['Config']['Env'] if '=' in value)
        if not private_env.get('POSTGRES_PASSWORD'):
            raise ValueError('数据库密码不可用，无法创建迁移数据库')
        env['POSTGRES_PASSWORD'] = private_env['POSTGRES_PASSWORD']
        run(['docker', 'run', '-d', '--name', stage, '--network', 'none', '-e', 'POSTGRES_PASSWORD', '-e', 'POSTGRES_DB=emby', '-e', 'POSTGRES_USER=emby', '-v', database_volume + ':/var/lib/postgresql/data', '-v', app_volume + ':/restore', database_info['Config']['Image']], env=env)
        wait(stage)
        run(['docker', 'cp', str(dump), stage + ':/tmp/migration.dump'])
        run(['docker', 'exec', stage, 'pg_restore', '--single-transaction', '--exit-on-error', '--no-owner', '-U', 'emby', '-d', 'emby', '/tmp/migration.dump'])
        with archive.open('rb') as source:
            subprocess.run(['docker', 'exec', '-i', stage, 'tar', '-xf', '-', '-C', '/restore'], stdin=source, stderr=subprocess.PIPE, check=True, timeout=600)
        query = ['psql', '-U', 'emby', '-d', 'emby', '-At', '-c', 'SELECT count(*) FROM items']
        before = run(['docker', 'exec', database, *query]).strip()
        if run(['docker', 'exec', stage, *query]).strip() != before:
            raise RuntimeError('迁移后的媒体条数不一致，原配置保持不变')
        run(['docker', 'stop', stage])
        save('prepared')
        # External named volumes survive removal of the Compose stack.
        temporary = compose_file.with_suffix('.storage.tmp')
        temporary.write_text(changed, encoding='utf-8')
        temporary.replace(compose_file)
        switched = True
        save('activating')
        compose('up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'postgres')
        active_database = compose('ps', '-q', 'postgres').decode().strip()
        wait(active_database)
        if run(['docker', 'exec', active_database, *query]).strip() != before:
            raise RuntimeError('切换后的媒体条数不一致，请使用迁移备份恢复')
        compose('up', '-d', '--no-deps', '--no-build', '--pull', 'never', service)
        active_app = compose('ps', '-q', service).decode().strip()
        for attempt in range(120):
            info = json.loads(run(['docker', 'inspect', active_app]))[0]
            if info['State'].get('Health', {}).get('Status') == 'healthy':
                save('complete')
                print('持久卷迁移完成，备份保存在：' + str(backup), flush=True)
                return
            time.sleep(1)
        raise RuntimeError('应用未恢复健康；已迁移的数据与备份保留，请检查服务日志')
    finally:
        if stopped and not switched:
            compose('start', service)
        if switched and state['phase'] != 'complete':
            print('迁移已切换配置，请保留当前配置和持久卷；不要切回可能已清空的内存数据库。', flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='将原部署的数据迁到 Docker 持久卷，保留数据库与应用备份')
    parser.add_argument('--project', type=Path, default=Path.cwd(), help='原 Compose 部署目录')
    try:
        migrate(parser.parse_args().project)
    except (RuntimeError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error) if isinstance(error, (RuntimeError, ValueError)) else '迁移失败，备份已保留；请检查部署目录 app-backups/storage')
