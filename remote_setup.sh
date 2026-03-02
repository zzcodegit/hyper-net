#!/usr/bin/env bash
set -euo pipefail

########################################
# НАСТРОЙКИ ПО УМОЛЧАНИЮ
########################################

DEFAULT_REMOTE_HOST="root@194.156.116.244"
DEFAULT_REMOTE_DIR="/opt/hypernet-node"

# Параметры можно переопределить:
#   ./remote_setup.sh root@155.212.216.226 /opt/hypernet-node
REMOTE_HOST="${1:-$DEFAULT_REMOTE_HOST}"
REMOTE_DIR="${2:-$DEFAULT_REMOTE_DIR}"

########################################

echo "[local] Синхронизирую проект на удалённую машину: $REMOTE_HOST:$REMOTE_DIR"
rsync -av --delete \
  --exclude '.git' \
  --exclude 'bin' \
  ./ "$REMOTE_HOST:$REMOTE_DIR/"

echo "[local] Запускаю удалённую настройку и проверки на $REMOTE_HOST..."
ssh "$REMOTE_HOST" bash -s <<EOF
set -euo pipefail

########################################
# Установка зависимостей на удалённой машине
########################################

echo "[remote] Обновляю пакеты и ставлю зависимости (docker, docker-compose, wget)..."
apt-get update -y
apt-get install -y docker.io docker-compose wget ca-certificates

echo "[remote] Ставлю Go 1.25 в /usr/local/go..."
mkdir -p /tmp/go-download
cd /tmp/go-download
rm -f go1.25.7.linux-amd64.tar.gz || true
wget -q https://go.dev/dl/go1.25.7.linux-amd64.tar.gz
rm -rf /usr/local/go
tar -C /usr/local -xzf go1.25.7.linux-amd64.tar.gz

export PATH=/usr/local/go/bin:\$PATH
export GOTOOLCHAIN=local

echo "[remote] Переход в каталог проекта: $REMOTE_DIR"
cd "$REMOTE_DIR"

echo "[remote] go version:"
go version

########################################
# Go: tidy, test, build
########################################

echo "[remote] go mod tidy..."
go mod tidy

echo "[remote] go test ./... (включая интеграционный тест ping)..."
go test ./...

echo "[remote] go build ./cmd/node -o bin/hypernet-node..."
mkdir -p bin
go build -o bin/hypernet-node ./cmd/node

########################################
# Тесты по чек-листу для бинаря
########################################

echo "[remote] Проверка: запуск ноды с разными портами и одним KEY_PATH..."
env KEY_PATH=test-node.key PORT=4001 timeout 10s bin/hypernet-node || true
env KEY_PATH=test-node.key PORT=4002 timeout 10s bin/hypernet-node || true

########################################
# Docker: образ и одиночный контейнер
########################################

echo "[remote] docker build -t hypernet-node ."
docker build -t hypernet-node .

echo "[remote] docker run --rm -p 4001:4001 hypernet-node (PORT=4001, KEY_PATH=/tmp/node.key)..."
CID=\$(docker run -d -p 4001:4001 -e PORT=4001 -e KEY_PATH=/tmp/node.key hypernet-node)
sleep 5
echo "[remote] Логи одиночного контейнера:"
docker logs "\$CID" || true
docker stop "\$CID" >/dev/null 2>&1 || true

########################################
# Docker Compose: несколько нод (node1, node2, relay)
########################################

echo "[remote] docker-compose up -d..."
docker-compose down || true
docker-compose up -d

echo "[remote] docker-compose ps:"
docker-compose ps

echo "[remote] Логи последних 50 строк от всех сервисов:"
docker-compose logs --tail=50 || true

echo "[remote] Проверка остановки одной ноды (node1) при работающих остальных..."
docker-compose stop node1 || true
sleep 5
docker-compose ps
docker-compose start node1 || true

echo "[remote] docker-compose ps после рестарта node1:"
docker-compose ps

echo "[remote] Оставляю стек поднятым. При необходимости выключи его командой:"
echo "         cd $REMOTE_DIR && docker-compose down"

echo "[remote] Все автоматизируемые шаги по плану (кроме GitHub) выполнены."
EOF

echo "[local] Скрипт remote_setup.sh завершил работу."

