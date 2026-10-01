# AI Emby

AI Emby 的维护仓库：**LLL198/ai-emby**。浅蓝白管理面板、设备长期登录、手动刮削并发、单库文件并发扫描及系统更新统一在这里维护。

## 源码结构

| 目录 | 内容 |
| --- | --- |
| `companion/` | 可编辑的服务源码及业务模块；实际运行时只启用手动刮削和扫库服务 |
| `gateway/` | 请求分流、登录保持、扫描状态汇总、系统更新接口 |
| `frontend/` | 当前线上前端 |
| `runtime/` | 尚未全部还原的核心服务运行文件、版本和 SHA256 |
| `scripts/` | 更新包构建、宿主机更新服务 |
| `docs/` | 架构与源码恢复状态 |

完整服务源码尚未全部还原。运行方式仍是网关 + 核心服务 + 扫描刮削服务，核心服务负责认证、授权、播放及其余功能。`companion/` 不能直接作为完整核心服务的替代品。

## 部署

当前支持 Linux amd64、Docker Compose、PostgreSQL 17。

```bash
git clone https://github.com/LLL198/ai-emby.git
cd ai-emby
cp .env.example .env
mkdir -p app-data app-backups postgres-data secrets media update-control
```

编辑 `.env`，填写数据库密码、至少 12 字节的管理员初始密码及 `MEDIA_PATH`。已有数据库不会通过这个变量重设管理员密码。

从源码构建并启动：

```bash
docker compose up -d --build
```

已有部署迁移时沿用 Compose 项目名、服务名、数据库、数据目录、媒体挂载、授权机器 ID 和端口，不要新建数据库覆盖现有部署。宿主机更新服务根据应用镜像识别 Compose 服务。

## 系统更新

面板「系统设置 → 检测更新」读取本仓库的最新正式 Release。更新包含核心服务运行文件、扫描刮削服务、网关和前端。下载后校验 SHA256，备份数据库和 Compose，切换镜像；启动失败时恢复旧镜像。

宿主机更新服务需要 Linux systemd、Python 3.11+、curl 和 Docker Compose。在部署目录执行：

```bash
sudo bash scripts/install-updater.sh "$PWD"
```

Compose 须包含 `./update-control:/app/update-control` 挂载。默认 root 容器可读写该目录；使用 PUID/PGID 时须给相应用户目录权限。应用容器通过任务目录提交请求，宿主机服务执行 Docker 更新。

更新遵循管理面板中启用的「更新」代理范围；检查更新和下载更新包均使用该代理。数据库备份和更新日志位于宿主机 `app-backups/updates/`，不应提交到仓库。

现有设备登录、授权及更新包继续使用稳定的协议格式，见 [兼容格式](docs/compatibility.md)。

更新时服务会短暂重启。若页面等待超过 15 分钟，可重新打开系统页查看后台状态。

## 发布新版本

版本格式：`YYYY.MM.DD-HHMMSS`，例如 `2026.10.01-165649`。仓库 Actions 的 **Publish release** 手动工作流构建并发布 Linux amd64 更新包及 GHCR 镜像。版本应递增，已有版本不覆盖；输入已发布版本时只使用该版本已有更新包发布 Docker 镜像。

本机构建更新包：

```bash
python3 scripts/package.py 2026.10.01-165649
```

输出位于 `dist/<版本>/`。同一个 Release 必须同时包含 `ai-emby-linux-amd64.tar.gz`、`update-linux-amd64.json` 和 `SHA256SUMS`；只上传源码不会被面板识别为可安装更新。

当前上传和发布流程只进行编译，不自动运行测试或扫描真实媒体。

## 恢复状态

恢复进度见 [文件状态清单](docs/source-recovery-status.csv)、[函数声明清单](docs/source-function-inventory.csv) 和 [2026-10-01 恢复记录](docs/recovery-2026-10-01.md)。源码命名、注释及结构的整理见 [源码整理记录](docs/source-cleanup-2026-10-01.md)。已有同名声明不代表功能已完整恢复，也不代表与原二进制等价。
