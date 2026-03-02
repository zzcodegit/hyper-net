#!/usr/bin/env bash
set -euo pipefail

# Этот скрипт запускается НА СЕРВЕРЕ (на 194.156.116.244) из каталога проекта.

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$PROJECT_DIR"

echo "[local] Каталог проекта: $PROJECT_DIR"

########################################
# Установка зависимостей
########################################

echo "[local] apt-get update && установка docker, docker-compose, wget..."
apt-get update -y
apt-get install -y docker.io docker-compose wget ca-certificates

echo "[local] Установка Go 1.25 в /usr/local/go..."
mkdir -p /tmp/go-download
cd /tmp/go-download
rm -f go1.25.7.linux-amd64.tar.gz || true
wget -q https://go.dev/dl/go1.25.7.linux-amd64.tar.gz
rm -rf /usr/local/go
tar -C /usr/local -xzf go1.25.7.linux-amd64.tar.gz

export PATH=/usr/local/go/bin:$PATH
export GOTOOLCHAIN=local

cd "$PROJECT_DIR"
echo "[local] go version:"
go version

########################################
# Go: tidy, test, build
########################################

echo "[local] go mod tidy..."
go mod tidy

echo "[local] go test ./... (включая интеграционный тест ping)..."
go test ./...

echo "[local] go build ./cmd/node -o bin/hypernet-node..."
mkdir -p bin
go build -o bin/hypernet-node ./cmd/node

########################################
# Тесты по чек-листу для бинаря
########################################

echo "[local] Проверка: запуск ноды с разными портами и одним KEY_PATH..."
env KEY_PATH=test-node.key PORT=4001 timeout 10s bin/hypernet-node || true
env KEY_PATH=test-node.key PORT=4002 timeout 10s bin/hypernet-node || true

########################################
# Docker: образ и одиночный контейнер
########################################

echo "[local] docker build -t hypernet-node ."
docker build -t hypernet-node .

echo "[local] docker run --rm -p 4001:4001 hypernet-node (PORT=4001, KEY_PATH=/tmp/node.key)..."
CID=$(docker run -d -p 4001:4001 -e PORT=4001 -e KEY_PATH=/tmp/node.key hypernet-node)
sleep 5
echo "[local] Логи одиночного контейнера:"
docker logs "$CID" || true
docker stop "$CID" >/dev/null 2>&1 || true

########################################
# Docker Compose: несколько нод (node1, node2, relay)
########################################

cd "$PROJECT_DIR"
echo "[local] docker-compose up -d..."
docker-compose down || true
docker-compose up -d

echo "[local] docker-compose ps:"
docker-compose ps

echo "[local] Логи последних 50 строк от всех сервисов:"
docker-compose logs --tail=50 || true

echo "[local] Проверка остановки одной ноды (node1) при работающих остальных..."
docker-compose stop node1 || true
sleep 5
docker-compose ps
docker-compose start node1 || true

echo "[local] docker-compose ps после рестарта node1:"
docker-compose ps

echo "[local] Стек остаётся поднятым. Остановить можно так:"
echo "        cd $PROJECT_DIR && docker-compose down"

echo "[local] local_setup.sh завершил работу."

