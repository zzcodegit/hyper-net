#!/usr/bin/env bash
set -euo pipefail

# Скрипт запускается на машине 85.198.66.158.
# По SSH берёт Peer ID/адрес ноды на удалённой машине
# и запускает локальную ноду, которая делает ping по /ping/1.0.0.

DEFAULT_REMOTE_HOST="root@194.156.116.244"
DEFAULT_REMOTE_DIR="/opt/hypernet-node"

# Можно переопределить:
#   ./remote_cross_ping.sh root@155.212.216.226 /opt/hypernet-node
REMOTE_HOST="${1:-$DEFAULT_REMOTE_HOST}"
REMOTE_DIR="${2:-$DEFAULT_REMOTE_DIR}"

# Публичный IP по умолчанию берём из REMOTE_HOST (часть после '@')
PUBLIC_IP_REMOTE="${REMOTE_HOST#*@}"

echo "[local] Получаю Peer ID и адрес node1 на удалённой машине $REMOTE_HOST..."

TARGET_ADDR=$(ssh "$REMOTE_HOST" bash -s <<EOF
set -euo pipefail
cd "$REMOTE_DIR"

PEER_ID=\$(docker logs hypernet-node-1 2>/dev/null | grep -m1 'Peer ID:' | awk '{print \$3}')
if [ -z "\$PEER_ID" ]; then
  echo ""
  exit 1
fi

echo "/ip4/$PUBLIC_IP_REMOTE/tcp/4001/p2p/\$PEER_ID"
EOF
)

if [ -z "${TARGET_ADDR:-}" ]; then
  echo "[local] Не удалось получить TARGET_ADDR с удалённой машины"
  exit 1
fi

echo "[local] TARGET_ADDR: $TARGET_ADDR"

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "[local] Убеждаюсь, что есть бинарь bin/hypernet-node..."
export PATH=/usr/local/go/bin:$PATH
export GOTOOLCHAIN=local
mkdir -p bin
if [ ! -x bin/hypernet-node ]; then
  go build -o bin/hypernet-node ./cmd/node
fi

echo "[local] Запускаю локальную ноду и делаю ping на удалённую..."
PORT=4002 KEY_PATH=test-node-cross.key \
  ./bin/hypernet-node \
    -target "$TARGET_ADDR" \
    -ping-count 3

echo "[local] Кросс-ping между 85.198.66.158 и 194.156.116.244 выполнен."

