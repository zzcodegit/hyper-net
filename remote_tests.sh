#!/usr/bin/env bash
set -euo pipefail

########################################
# НАСТРОЙКИ ПО УМОЛЧАНИЮ
########################################

DEFAULT_REMOTE_HOST="root@194.156.116.244"
DEFAULT_REMOTE_DIR="/opt/hypernet-node"

# Можно переопределить:
#   ./remote_tests.sh root@155.212.216.226 /opt/hypernet-node
REMOTE_HOST="${1:-$DEFAULT_REMOTE_HOST}"
REMOTE_DIR="${2:-$DEFAULT_REMOTE_DIR}"

########################################

echo "[local] Запускаю тесты по plan_one.md на удалённой машине: $REMOTE_HOST"

ssh "$REMOTE_HOST" bash -s <<EOF
set -euo pipefail

echo "[remote] Переход в каталог проекта: $REMOTE_DIR"
cd "$REMOTE_DIR"

echo "[remote] Проверка go test ./... (на всякий случай)"
export PATH=/usr/local/go/bin:\$PATH
export GOTOOLCHAIN=local
go test ./...

echo
echo "[remote] Состояние docker-compose стека:"
docker-compose ps

echo
echo "[remote] Получаю Peer ID и адрес node1..."
PEER_ID=\$(docker logs hypernet-node-1 2>/dev/null | grep -m1 'Peer ID:' | awk '{print \$3}')
if [ -z "\$PEER_ID" ]; then
  echo "[remote] Не удалось найти Peer ID в логах hypernet-node-1"
  exit 1
fi

NODE1_IP=\$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' hypernet-node-1)
if [ -z "\$NODE1_IP" ]; then
  echo "[remote] Не удалось получить внутренний IP hypernet-node-1"
  exit 1
fi

TARGET_ADDR="/ip4/\$NODE1_IP/tcp/4001/p2p/\$PEER_ID"
echo "[remote] TARGET_ADDR для ping из node2: \$TARGET_ADDR"

echo
echo "[remote] Запускаю ping из node2 в node1 по протоколу /ping/1.0.0..."
docker exec hypernet-node-2 hypernet-node -target "\$TARGET_ADDR" -ping-count 3

echo
echo "[remote] Проверяю отказоустойчивость: останавливаю node1, остальные должны работать..."
docker-compose stop node1 || true
sleep 5
docker-compose ps

echo "[remote] Запускаю node1 обратно..."
docker-compose start node1 || true
sleep 5
docker-compose ps

echo
echo "[remote] Последние 50 строк логов по всем сервисам (для проверки соединений/DHT)..."
docker-compose logs --tail=50 || true

echo
echo "[remote] Тестовый сценарий по plan_one.md (ping между нодами, проверка compose) выполнен."
EOF

echo "[local] Скрипт remote_tests.sh завершил работу."

