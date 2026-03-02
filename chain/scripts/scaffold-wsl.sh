#!/bin/sh
# Запуск из корня репозитория: wsl sh chain/scripts/scaffold-wsl.sh
# Устанавливает Ignite CLI (если нет) и создаёт chain/hypernet/
# Требуется WSL с curl (например Ubuntu из Microsoft Store).

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CHAIN_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)/chain"
cd "$CHAIN_DIR"

if ! command -v ignite >/dev/null 2>&1; then
  if ! command -v curl >/dev/null 2>&1; then
    echo "В этом WSL нет curl (и, скорее всего, нет Ignite)."
    echo "Установи дистрибутив Ubuntu: в PowerShell выполни  wsl --install -d Ubuntu"
    echo "После установки открой Ubuntu, перейди в папку проекта и снова запусти:"
    echo "  wsl -d Ubuntu sh chain/scripts/scaffold-wsl.sh"
    exit 1
  fi
  echo "Ignite CLI не найден. Устанавливаю..."
  RUNNER="sh"
  command -v bash >/dev/null 2>&1 && RUNNER="bash"
  if ! curl -sSfL https://get.ignite.com/cli | $RUNNER; then
    echo "Установка не удалась. Используй Ubuntu: wsl --install -d Ubuntu"
    exit 1
  fi
  export PATH="$HOME/bin:/usr/local/bin:$PATH"
fi
if ! command -v ignite >/dev/null 2>&1; then
  echo "Ignite всё ещё не найден. Добавь в PATH: export PATH=\"\$HOME/bin:/usr/local/bin:\$PATH\" и запусти скрипт снова. Либо используй Ubuntu WSL."
  exit 1
fi

if [ ! -d "hypernet" ]; then
  echo "Создаю блокчейн: ignite scaffold chain hypernet --no-module"
  ignite scaffold chain hypernet --no-module
  echo "Готово. Проект: $CHAIN_DIR/hypernet"
else
  echo "Папка hypernet уже существует. Удалите её и запустите скрипт снова для пересоздания."
fi
