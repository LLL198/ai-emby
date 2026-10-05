#!/usr/bin/env bash
set -euo pipefail
PROJECT="${1:-/opt/ai-emby}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
test "$(id -u)" = 0 || { echo '请以 root 运行安装脚本'; exit 1; }
test -f "$PROJECT/compose.yaml"
command -v python3 >/dev/null
command -v curl >/dev/null
command -v docker >/dev/null
install -d -m 700 "$PROJECT/update-control"
if test -e "$PROJECT/update-control/processing.json" || test -e "$PROJECT/update-control/request.json"; then
  echo '有等待或正在执行的更新，请完成后再安装更新服务'
  exit 1
fi
install -m 700 "$SCRIPT_DIR/updater.py" "$PROJECT/update-control/updater.py"
cat > /etc/systemd/system/ai-emby-updater.service <<EOF
[Unit]
Description=AI Emby release update worker
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/python3 "$PROJECT/update-control/updater.py" --project "$PROJECT"
Restart=on-failure
RestartSec=5
UMask=0077

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable ai-emby-updater.service
systemctl restart ai-emby-updater.service
echo '更新服务已安装。Compose 中需挂载 ./update-control:/app/update-control，详见 README。'
