#!/usr/bin/env bash
# install.sh — 在 Ubuntu / CentOS Stream 上把 gitwo 安装为 systemd 服务。
#
#   sudo GITWO_BIN=gitwo-linux-amd64 ./scripts/install.sh
#
# Gitwo 是自包含平台（git 服务端 + Web UI + 流水线 + 站点托管），
# 零配置启动：默认监听 :9527，首次运行自动创建管理员 admin/admin。
# 程序目录即数据目录（repos/ workspaces/ apps/ data/）；临时文件在 /tmp/gitwo/。
set -euo pipefail

INSTALL_DIR=/opt/gitwo
SERVICE_FILE=/etc/systemd/system/gitwo.service
RUN_USER=gitwo

[ "$(id -u)" -eq 0 ] || { echo "must run as root"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC="${GITWO_BIN:-$SCRIPT_DIR/../gitwo}"
[ -x "$BIN_SRC" ] || { echo "gitwo binary not found: $BIN_SRC (build: GOOS=linux go build -o gitwo .)"; exit 1; }

# ---------- 系统依赖 ----------
if command -v apt-get >/dev/null 2>&1; then
  apt-get update -y
  apt-get install -y git curl
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y git curl
else
  echo "WARNING: neither apt-get nor dnf found; make sure git is installed" >&2
fi

# ---------- 专用低权限用户 ----------
NOLOGIN=/usr/sbin/nologin
[ -x "$NOLOGIN" ] || NOLOGIN=/sbin/nologin
if id "$RUN_USER" >/dev/null 2>&1; then
  echo "user $RUN_USER exists"
else
  useradd --system --home-dir "$INSTALL_DIR" --shell "$NOLOGIN" "$RUN_USER"
fi

# ---------- 目录与二进制 ----------
mkdir -p "$INSTALL_DIR"
install -m 0755 "$BIN_SRC" "$INSTALL_DIR/gitwo"
chown -R "$RUN_USER:$RUN_USER" "$INSTALL_DIR"

# ---------- systemd ----------
install -m 0644 "$SCRIPT_DIR/../deploy/gitwo.service" "$SERVICE_FILE"
systemctl daemon-reload
systemctl enable gitwo

echo
echo "================ 安装完成 ================"
echo "程序目录:   $INSTALL_DIR            （即数据目录：repos/ workspaces/ apps/ data/）"
echo "临时文件:   /tmp/gitwo/          （构建临时目录与任务日志，重启清空）"
echo "启动:       systemctl enable --now gitwo"
echo
echo "启动后:"
echo "  Web UI : http://<本机地址>:9527/   （默认账户 admin / admin，登录后立即在右上角修改）"
echo "  Git    : http://<本机地址>:9527/git/<仓库名>.git"
echo "  流水线 : http://<本机地址>:9527/pipelines"
echo "  站点   : http://<本机地址>:9527/sites/<仓库名>/"
echo
if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce)" = "Enforcing" ]; then
  echo "提示: SELinux 为 Enforcing。默认 targeted 策略下系统服务可写 /opt 与 /tmp；"
  echo "      若有 AVC 拒绝，用 ausearch -m avc -ts recent 排查。"
fi
if command -v firewall-cmd >/dev/null 2>&1 && systemctl is-active firewalld >/dev/null 2>&1; then
  echo "提示: firewalld 运行中，放行: firewall-cmd --permanent --add-port=9527/tcp && firewall-cmd --reload"
  echo "      部署的服务还会占用 20000-23999 范围内的端口，按需放行。"
fi
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q active; then
  echo "提示: ufw 启用中，放行: ufw allow 9527/tcp"
fi
