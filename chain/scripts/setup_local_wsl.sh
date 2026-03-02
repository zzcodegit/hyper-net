#!/bin/sh
# Полная локальная установка в WSL Ubuntu: Go, Ignite, scaffold цепочки.
# Из PowerShell (из папки репо): wsl -d Ubuntu sh chain/scripts/setup_local_wsl.sh
# Из WSL Ubuntu: cd /mnt/c/hypernet-node && sh chain/scripts/setup_local_wsl.sh

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CHAIN_DIR="$REPO_ROOT/chain"
cd "$CHAIN_DIR"

echo "[local] Проверка Go..."
if ! command -v go >/dev/null 2>&1; then
  echo "[local] Устанавливаю Go 1.21 в \$HOME/go..."
  GO_VER="1.21.5"
  GO_TAR="go${GO_VER}.linux-amd64.tar.gz"
  mkdir -p "$HOME/go"
  if command -v wget >/dev/null 2>&1; then
    wget -q "https://go.dev/dl/${GO_TAR}" -O "/tmp/${GO_TAR}"
  else
    curl -sSL "https://go.dev/dl/${GO_TAR}" -o "/tmp/${GO_TAR}"
  fi
  rm -rf "$HOME/go/go"
  tar -C "$HOME/go" -xzf "/tmp/${GO_TAR}"
  rm -f "/tmp/${GO_TAR}"
  export PATH="$HOME/go/go/bin:$PATH"
else
  export PATH="/usr/local/go/bin:$PATH"
  [ -d "$HOME/go/go/bin" ] && export PATH="$HOME/go/go/bin:$PATH"
fi
export PATH="/usr/local/go/bin:$HOME/go/go/bin:$PATH"
echo "[local] Go: $(go version)"

echo "[local] Проверка Ignite..."
if ! command -v ignite >/dev/null 2>&1; then
  echo "[local] Устанавливаю Ignite CLI..."
  if ! command -v curl >/dev/null 2>&1; then
    echo "[local] Нужен curl. Выполни: sudo apt-get update && sudo apt-get install -y curl"
    exit 1
  fi
  curl -sSfL https://get.ignite.com/cli | bash
  export PATH="$(pwd):$HOME/bin:/usr/local/bin:$PATH"
fi
if [ -f "$CHAIN_DIR/ignite" ]; then
  chmod +x "$CHAIN_DIR/ignite" 2>/dev/null || true
  export PATH="$CHAIN_DIR:$PATH"
fi
if ! command -v ignite >/dev/null 2>&1; then
  echo "[local] Ignite не найден в PATH. Добавь в ~/.bashrc: export PATH=\"$CHAIN_DIR:\$HOME/bin:/usr/local/go/bin:\$PATH\""
  exit 1
fi
echo "[local] Ignite: $(ignite version 2>/dev/null || true)"

if [ ! -d "hypernet" ]; then
  echo "[local] Создаю цепочку: ignite scaffold chain hypernet --no-module"
  ignite scaffold chain hypernet --no-module
  echo "[local] Готово. Цепочка: $CHAIN_DIR/hypernet"
else
  echo "[local] Папка hypernet уже есть. Для пересоздания удали её и запусти скрипт снова."
fi

echo ""
echo "Дальше: cd $CHAIN_DIR/hypernet && ignite chain build"
echo "При 403 от buf.build задай: export BUF_TOKEN=твой_токен (токен на https://buf.build/settings/user)"
echo "Запуск ноды: ignite chain serve"
