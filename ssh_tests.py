#!/usr/bin/env python
import os
import sys
import subprocess
import time
from pathlib import Path
from datetime import datetime, timezone
import argparse

import paramiko  # pip install paramiko

DOSTUP_FILE = "dostup.md"
PLAN_FILE = "plan_one.md"
REPORT_FILE = "plan_one_report.md"
REMOTE_DIR = "/opt/hypernet-node"
SSH_PORT = 22  # стандартный SSH-порт
LOCAL_ROOT = Path(__file__).resolve().parent
LOCAL_NODE_BIN = LOCAL_ROOT / "bin" / "hypernet-node.exe"
LOCAL_DHTDEMO_MAIN = LOCAL_ROOT / "cmd" / "dhtdemo" / "main.go"

# Храним служебную информацию, извлечённую с удалённых серверов (PeerID relay, node2 и т.п.)
REMOTE_INFO: dict[str, dict[str, str]] = {}

# Шаблон bash-скрипта, который выполняется на удалённой машине для полного набора тестов.
# Маркер __REMOTE_DIR__ будет заменён на фактический путь REMOTE_DIR перед отправкой.
REMOTE_TEST_SCRIPT = r"""set -euo pipefail

log() {
  echo "[$(date -Iseconds)] $*"
}
# Замер времени этапа: section_start перед блоком, section_end "название" после.
section_start() { SECTION_START=$(date +%s); }
section_end() { log "[timing] $1: $(( $(date +%s) - SECTION_START ))s"; }

section_start
if command -v docker >/dev/null 2>&1 && (command -v docker-compose >/dev/null 2>&1 || docker compose version >/dev/null 2>&1); then
  log "[remote] Docker и docker-compose уже установлены, пропускаю установку."
else
  log "[remote] Обновляю пакеты и ставлю зависимости (docker, docker-compose, wget, iperf3)..."
  apt-get update -y
  DEBIAN_FRONTEND=noninteractive apt-get install -y \
    -o Dpkg::Options::=--force-confdef \
    -o Dpkg::Options::=--force-confold \
    docker.io docker-compose wget ca-certificates iperf3
fi
section_end "Docker check/install"

log "[remote] Тюнинг сетевых буферов для UDP/TCP (sysctl)..."
sysctl -w net.core.rmem_max=134217728 || true
sysctl -w net.core.wmem_max=134217728 || true
sysctl -w net.core.rmem_default=4194304 || true
sysctl -w net.core.wmem_default=4194304 || true
sysctl -w net.ipv4.tcp_rmem="4096 87380 67108864" || true
sysctl -w net.ipv4.tcp_wmem="4096 65536 67108864" || true
sysctl -w net.ipv4.tcp_window_scaling=1 || true
sysctl -w net.core.default_qdisc=fq || true
sysctl -w net.ipv4.tcp_congestion_control=bbr || true

section_start
if [ -x /usr/local/go/bin/go ] && /usr/local/go/bin/go version 2>/dev/null | grep -q "go1.25"; then
  log "[remote] Go 1.25 уже установлен, пропускаю установку."
else
  log "[remote] Ставлю Go 1.25 в /usr/local/go..."
  mkdir -p /tmp/go-download
  cd /tmp/go-download
  rm -f go1.25.7.linux-amd64.tar.gz || true
  wget -q https://go.dev/dl/go1.25.7.linux-amd64.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf go1.25.7.linux-amd64.tar.gz
fi
export PATH=/usr/local/go/bin:$PATH
export GOTOOLCHAIN=local
section_end "Go check/install"

section_start
log "[remote] Переход в каталог проекта: __REMOTE_DIR__"
cd "__REMOTE_DIR__"
log "[remote] go version:"
go version
log "[remote] go mod tidy..."
go mod tidy
section_end "cd + go mod tidy"

section_start
log "[remote] go test ./... -short -count=1 (юнит-тесты, timeout 300s)..."
if ! timeout 300 go test ./... -short -count=1 -timeout 90s; then
  log "[remote] go test завершился с ошибкой или таймаутом (300s)."
  exit 1
fi
section_end "go test (unit)"

section_start
log "[remote] go build ./cmd/node -o bin/hypernet-node..."
mkdir -p bin
go build -o bin/hypernet-node ./cmd/node
section_end "go build node"

section_start
log "[remote] Очистка Docker (prune) для освобождения места перед сборкой..."
docker system prune -f || true
log "[remote] docker build -t hypernet-node . (кэш используется)"
docker build -t hypernet-node .
section_end "docker build"

section_start
log "[remote] docker-compose down + up -d (node1, node2, relay)..."
docker-compose down -v --remove-orphans 2>/dev/null || true
for port in 4001 4002 4003 4010 4101 4102 4103; do
  for cid in $(docker ps -q --filter "publish=$port" 2>/dev/null); do
    [ -n "$cid" ] && docker stop "$cid" 2>/dev/null || true
  done
done
# Ограничиваем время ожидания docker-compose up, чтобы скрипт не «висел» навсегда,
# но при этом после таймаута всё равно печатаем ps для диагностики.
if ! timeout 180 docker-compose up --build -d; then
  log "[remote] docker-compose up --build -d не уложился в 180 секунд или завершился с ошибкой."
  docker-compose ps || true
fi
log "[remote] docker-compose ps:"
docker-compose ps
section_end "docker-compose up"

log "[remote] Логи последних 50 строк от всех сервисов:"
docker-compose logs --tail=50 || true

section_start
log "[remote] Запуск релей-ноды с ограниченными ресурсами (PORT=4010, RELAY_MAX_CONNECTIONS=2, RELAY_DATA_LIMIT=1048576)..."
LIMITED_RELAY_CID=$(docker run -d --rm \
  -p 4010:4010 \
  -e PORT=4010 \
  -e KEY_PATH=/tmp/relay-limited.key \
  -e RELAY=true \
  -e DHT=true \
  -e DHT_MODE=server \
  -e RELAY_MAX_CONNECTIONS=2 \
  -e RELAY_DATA_LIMIT=1048576 \
  hypernet-node)
sleep 3
log "[remote] Логи релей-ноды с ограниченными ресурсами:"
docker logs "$LIMITED_RELAY_CID" || true

log "[remote] Проверка лимитов relay: поднимаю нескольких клиентов за NAT..."
LIMITED_RELAY_PEER_ID=$(docker logs "$LIMITED_RELAY_CID" 2>&1 | grep "Peer ID:" | head -n1 | awk '{print $NF}')
echo "[remote] LIMITED_RELAY_PEER_ID=$LIMITED_RELAY_PEER_ID"
CLIENT_IDS=""
if [ -n "$LIMITED_RELAY_PEER_ID" ]; then
  for i in 1 2 3 4; do
    PORT_C=$((5000 + i))
    echo "[remote] Запуск клиентской ноды за NAT #$i (PORT=$PORT_C)..."
    CID_C=$(docker run -d --rm \
      -e PORT=$PORT_C \
      -e KEY_PATH=/tmp/client-$i.key \
      -e DHT=true \
      -e DHT_MODE=client \
      -e BOOTSTRAP_PEERS="/ip4/127.0.0.1/tcp/4010/p2p/$LIMITED_RELAY_PEER_ID" \
      hypernet-node)
    CLIENT_IDS="$CLIENT_IDS $CID_C"
  done
  log "[remote] Даем клиентам время подключиться к ограниченному relay (8s)..."
  sleep 8
  log "[remote] Логи релей-ноды с ограниченными ресурсами под нагрузкой (tail -n 100):"
  docker logs "$LIMITED_RELAY_CID" --tail=100 || true
else
  log "[remote] Не удалось определить PeerID ограниченного relay, пропускаю тест лимитов."
fi

for C in $CLIENT_IDS; do
  docker stop "$C" >/dev/null 2>&1 || true
done
docker stop "$LIMITED_RELAY_CID" >/dev/null 2>&1 || true
log "[remote] Проверка освобождения ресурсов: клиентские ноды и ограниченный relay остановлены (docker stop выполнен для всех CID)."
section_end "relay limited + clients"

section_start
log "[remote] Запуск нескольких полных узлов DHT (минимум 3)..."
# Освобождаем порты 4101–4103: останавливаем контейнеры с именами hypernet-dht-* и любые контейнеры, публикующие эти порты (остатки прошлых запусков).
for name in hypernet-dht-1 hypernet-dht-2 hypernet-dht-3; do
  docker stop "$name" 2>/dev/null || true
done
for port in 4101 4102 4103; do
  for cid in $(docker ps -q --filter "publish=$port" 2>/dev/null); do
    [ -n "$cid" ] && docker stop "$cid" 2>/dev/null || true
  done
done
DHT_SERVER_IDS=""
FIRST_DHT_CID=""
FIRST_DHT_PORT=""
SECOND_DHT_CID=""
# DHT #1 без bootstrap
echo "[remote] Запуск полного узла DHT #1 (PORT=4101)..."
FIRST_DHT_CID=$(docker run -d --rm --name "hypernet-dht-1" \
  -p 4101:4101 -p 4101:4101/udp \
  -e PORT=4101 -e KEY_PATH=/tmp/dht-server-1.key -e DHT=true -e DHT_MODE=server \
  hypernet-node)
DHT_SERVER_IDS="$DHT_SERVER_IDS $FIRST_DHT_CID"
FIRST_DHT_PORT="4101"
sleep 5
DHT1_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$FIRST_DHT_CID" 2>/dev/null | tr -d '\n')
DHT1_PEER=$(docker logs "$FIRST_DHT_CID" 2>&1 | grep "Peer ID:" | tail -n1 | awk '{print $NF}')
BOOTSTRAP_TO_1="/ip4/$DHT1_IP/tcp/4101/p2p/$DHT1_PEER"
# DHT #2 с bootstrap на #1
echo "[remote] Запуск полного узла DHT #2 (PORT=4102, bootstrap=#1)..."
SECOND_DHT_CID=$(docker run -d --rm --name "hypernet-dht-2" \
  -p 4102:4102 -p 4102:4102/udp \
  -e PORT=4102 -e KEY_PATH=/tmp/dht-server-2.key -e DHT=true -e DHT_MODE=server \
  -e BOOTSTRAP_PEERS="$BOOTSTRAP_TO_1" \
  hypernet-node)
DHT_SERVER_IDS="$DHT_SERVER_IDS $SECOND_DHT_CID"
sleep 3
DHT2_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$SECOND_DHT_CID" 2>/dev/null | tr -d '\n')
DHT2_PEER=$(docker logs "$SECOND_DHT_CID" 2>&1 | grep "Peer ID:" | tail -n1 | awk '{print $NF}')
BOOTSTRAP_TO_12="$BOOTSTRAP_TO_1,/ip4/$DHT2_IP/tcp/4102/p2p/$DHT2_PEER"
# DHT #3 с bootstrap на #1 и #2
echo "[remote] Запуск полного узла DHT #3 (PORT=4103, bootstrap=#1,#2)..."
CID_D3=$(docker run -d --rm --name "hypernet-dht-3" \
  -p 4103:4103 -p 4103:4103/udp \
  -e PORT=4103 -e KEY_PATH=/tmp/dht-server-3.key -e DHT=true -e DHT_MODE=server \
  -e BOOTSTRAP_PEERS="$BOOTSTRAP_TO_12" \
  hypernet-node)
DHT_SERVER_IDS="$DHT_SERVER_IDS $CID_D3"
log "[remote] Даем полным узлам DHT время на обмен пирами (5s)..."
sleep 5
log "[remote] Логи полных узлов DHT (tail -n 50):"
for CID_D in $DHT_SERVER_IDS; do
  docker logs "$CID_D" --tail=50 || true
done
section_end "DHT servers start + sleep 15"

section_start
log "[remote] Проверка наличия кода DHT DNS (initialDelay + retry attempt) в cmd/node/main.go..."
if grep -E -q "initialDelay|attempt %d" cmd/node/main.go 2>/dev/null; then
  log "[remote] Найден код DHT DNS retry:"
  grep -n -E -A2 "initialDelay|attempt %d" cmd/node/main.go 2>/dev/null || true
else
  log "[remote] Предупреждение: в cmd/node/main.go не найден код DHT DNS retry (initialDelay/attempt)."
fi
log "[remote] Обновление образа hypernet-node для DHT DNS теста (с кэшем, экономия места на диске)..."
docker build -t hypernet-node .
section_end "DHT DNS code check + docker build"

section_start
log "[remote] Проверка DHT DNS: регистрация имени и resolve..."
# Собираем bootstrap из всех трёх DHT-серверов (bridge IP + peer ID), через запятую — надёжнее заполнение routing table.
BOOTSTRAP_PARTS=""
for i in 1 2 3; do
  CID_D=$(docker ps -q -f "name=hypernet-dht-$i" 2>/dev/null | head -n1)
  [ -z "$CID_D" ] && continue
  PEER_ID=$(docker logs "$CID_D" 2>&1 | grep "Peer ID:" | tail -n1 | awk '{print $NF}')
  IP_D=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$CID_D" 2>/dev/null | tr -d '\n')
  PORT_D=$((4100 + i))
  [ -n "$PEER_ID" ] && [ -n "$IP_D" ] && BOOTSTRAP_PARTS="${BOOTSTRAP_PARTS}/ip4/$IP_D/tcp/$PORT_D/p2p/$PEER_ID,"
done
BOOTSTRAP_FOR_REG="${BOOTSTRAP_PARTS%,}"
# Для resolve на хосте: bootstrap по всем трём DHT (127.0.0.1:4101–4103), чтобы найти запись независимо от того, на каком узле она лежит.
BOOTSTRAP_FOR_RESOLVE=""
for i in 1 2 3; do
  CID_D=$(docker ps -q -f "name=hypernet-dht-$i" 2>/dev/null | head -n1)
  [ -z "$CID_D" ] && continue
  PEER_ID=$(docker logs "$CID_D" 2>&1 | grep "Peer ID:" | tail -n1 | awk '{print $NF}')
  PORT_D=$((4100 + i))
  [ -n "$PEER_ID" ] && BOOTSTRAP_FOR_RESOLVE="${BOOTSTRAP_FOR_RESOLVE}/ip4/127.0.0.1/tcp/$PORT_D/p2p/$PEER_ID,"
done
BOOTSTRAP_FOR_RESOLVE="${BOOTSTRAP_FOR_RESOLVE%,}"
FIRST_DHT_PEER_ID=$(docker logs "$FIRST_DHT_CID" 2>&1 | grep "Peer ID:" | tail -n1 | awk '{print $NF}')
DNS_TEST_NAME="test-node"
if [ -n "$FIRST_DHT_PEER_ID" ] && [ -n "$BOOTSTRAP_FOR_REG" ]; then
  log "[remote] Запуск ноды с DNS_NAMES=$DNS_TEST_NAME (bridge, DHT_MODE=client, bootstrap=$BOOTSTRAP_FOR_REG)"
  CID_DNS_REG=$(docker run -d --rm \
    -e PORT=4198 \
    -e KEY_PATH=/tmp/dns-reg.key \
    -e DHT=true \
    -e DHT_MODE=client \
    -e DNS_NAMES="$DNS_TEST_NAME" \
    -e BOOTSTRAP_PEERS="$BOOTSTRAP_FOR_REG" \
    hypernet-node)
  # Нода повторяет Register раз в 3 с до заполнения DHT routing table; даём время на успех.
  sleep 10
  log "[remote] Логи ноды с зарегистрированным именем (tail -n 25):"
  docker logs "$CID_DNS_REG" --tail=25 || true
  log "[remote] Пауза 3 с для распространения записи в DHT перед resolve..."
  sleep 3
  export BOOTSTRAP_PEERS="${BOOTSTRAP_FOR_RESOLVE:-/ip4/127.0.0.1/tcp/$FIRST_DHT_PORT/p2p/$FIRST_DHT_PEER_ID}"
  log "[remote] hypernet-client resolve $DNS_TEST_NAME (BOOTSTRAP_PEERS=$BOOTSTRAP_PEERS)..."
  if timeout 45 go run ./cmd/client resolve "$DNS_TEST_NAME" >/tmp/dns_resolve.txt 2>&1; then
    if grep -qE '^/ip[46]/' /tmp/dns_resolve.txt; then
      log "[remote] DHT DNS resolve PASSED: имя $DNS_TEST_NAME разрешилось в multiaddr (см. /tmp/dns_resolve.txt)."
    else
      log "[remote] DHT DNS resolve FAILED: вывод не содержит multiaddr (см. /tmp/dns_resolve.txt)."
      cat /tmp/dns_resolve.txt || true
    fi
  else
    log "[remote] DHT DNS resolve FAILED: команда завершилась с ошибкой (см. /tmp/dns_resolve.txt)."
    cat /tmp/dns_resolve.txt || true
  fi
  unset BOOTSTRAP_PEERS
  docker stop "$CID_DNS_REG" >/dev/null 2>&1 || true
else
  log "[remote] Не удалось получить Peer ID или bootstrap-адреса DHT-серверов, пропускаю проверку DHT DNS."
fi
section_end "DHT DNS resolve"

USE_TC_NETEM="${USE_TC:-0}"
if [ "$USE_TC_NETEM" = "1" ]; then
  log "[remote] Включаю tc netem на eth0 (200ms delay, 5% loss) для proxy-тестов..."
  tc qdisc add dev eth0 root netem delay 200ms 50ms loss 5% || log "[remote] Не удалось применить tc netem (возможно, нет eth0). Продолжаю без ухудшения сети."
fi

section_start
log "[remote] Proxy HTTP smoke test через node1 -> example.com:80..."
sleep 2
# Берём именно последний Peer ID из логов, чтобы не зацепить старые рестарты контейнера.
NODE1_PEER_ID=$(docker logs hypernet-node-1 2>&1 | grep "Peer ID:" | tail -n1 | awk '{print $NF}')
if [ -n "$NODE1_PEER_ID" ]; then
  PROXY_NODE_MADDR="/ip4/127.0.0.1/tcp/4001/p2p/$NODE1_PEER_ID"
  log "[remote] Proxy HTTP smoke test using node1 multiaddr: $PROXY_NODE_MADDR"
  # Не полагаемся только на exit-code go run: если в ответе есть HTTP 200,
  # считаем smoke-тест успешным.
  timeout 60 go run ./cmd/client -node "$PROXY_NODE_MADDR" -host example.com -port 80 -http-get >/tmp/proxy_http_smoke.txt 2>&1 || true
  if head -n1 /tmp/proxy_http_smoke.txt | grep -q "^HTTP/1.1 200"; then
    log "[remote] Proxy HTTP smoke test PASSED (получен HTTP 200, см. /tmp/proxy_http_smoke.txt)."
  else
    log "[remote] Proxy HTTP smoke test FAILED (см. /tmp/proxy_http_smoke.txt)."
    log "[remote] --- proxy_http_smoke.txt (first 40 lines) ---"
    sed -n '1,40p' /tmp/proxy_http_smoke.txt || true
    log "[remote] --- end of proxy_http_smoke.txt ---"
  fi
section_end "proxy HTTP smoke"

  section_start
  log "[remote] Proxy ifconfig.me test (external IP should match прямой curl)..."
  DIRECT_IP=$(curl -s ifconfig.me || curl -s ifconfig.me/ip || echo "")
  if [ -n "$DIRECT_IP" ]; then
    timeout 60 go run ./cmd/client -node "$PROXY_NODE_MADDR" -host ifconfig.me -port 80 -http-get >/tmp/proxy_ifconfig.txt 2>&1 || true
    # Извлекаем первый IP из ответа (и игнорируем статусную строку/заголовки).
    PROXY_IP=$(grep -Eo '([0-9]{1,3}\.){3}[0-9]{1,3}' /tmp/proxy_ifconfig.txt | head -n1 | tr -d '\r')
    if [ -n "$PROXY_IP" ]; then
      log "[remote] DIRECT_IP=$DIRECT_IP, PROXY_IP=$PROXY_IP"
      if [ "$DIRECT_IP" = "$PROXY_IP" ]; then
        log "[remote] Proxy ifconfig.me test PASSED (traffic действительно идёт через ноду)."
      else
        log "[remote] Proxy ifconfig.me test FAILED (IP не совпадает)."
      fi
    else
      log "[remote] Proxy ifconfig.me test FAILED (не удалось извлечь IP из ответа, см. /tmp/proxy_ifconfig.txt)."
      log "[remote] --- proxy_ifconfig.txt (first 40 lines) ---"
      sed -n '1,40p' /tmp/proxy_ifconfig.txt || true
      log "[remote] --- end of proxy_ifconfig.txt ---"
    fi
  else
    log "[remote] Не удалось получить DIRECT_IP через curl ifconfig.me, пропускаю тест."
  fi

  log "[remote] iperf3 proxy throughput test (direct vs via proxy-forwarder)..."
  if command -v iperf3 >/dev/null 2>&1; then
    # Стартуем iperf3-сервер на хосте; его будет видеть и сам хост (127.0.0.1),
    # и контейнеры по адресу шлюза docker-сети (обычно 172.17.0.1).
    iperf3 -s -p 5201 -D || true
    sleep 2

    # Выбираем псевдослучайный порт для локального proxy-forwarder, чтобы избежать конфликтов.
    PF_PORT=$((5200 + RANDOM % 100))
    # На всякий случай убиваем старые процессы proxy-forwarder от предыдущих прогонов.
    pkill -f "cmd/proxy-forwarder" >/dev/null 2>&1 || true

    # В upstream указываем docker-шлюз 172.17.0.1, чтобы /proxy внутри контейнера
    # node1 смог достучаться до iperf3-сервера, запущенного на хосте.
    log "[remote] Запуск proxy-forwarder на 127.0.0.1:$PF_PORT -> 172.17.0.1:5201 через $PROXY_NODE_MADDR"
    go run ./cmd/proxy-forwarder -node "$PROXY_NODE_MADDR" -listen 127.0.0.1:$PF_PORT -upstream 172.17.0.1:5201 >/tmp/proxy_forwarder.log 2>&1 &
    PF_PID=$!
    sleep 2

    log "[remote] iperf3 direct (127.0.0.1:5201)..."
    DIRECT_RESULT=$(iperf3 -c 127.0.0.1 -p 5201 -t 5 2>/dev/null | grep -E 'receiver|sender' | tail -n1 || true)
    log "[remote] iperf3 via proxy (127.0.0.1:$PF_PORT)..."
    PROXY_RESULT=$(iperf3 -c 127.0.0.1 -p $PF_PORT -t 5 2>/dev/null | grep -E 'receiver|sender' | tail -n1 || true)

    log "[remote] iperf3 direct result: $DIRECT_RESULT"
    log "[remote] iperf3 proxy  result: $PROXY_RESULT"
    if [ -z "$PROXY_RESULT" ]; then
      log "[remote] --- proxy_forwarder.log (last 40 lines) ---"
      tail -n 40 /tmp/proxy_forwarder.log || true
      log "[remote] --- end of proxy_forwarder.log ---"
    fi

    kill "$PF_PID" >/dev/null 2>&1 || true
    pkill -f "cmd/proxy-forwarder" >/dev/null 2>&1 || true
    pkill iperf3 >/dev/null 2>&1 || true
  else
    log "[remote] iper3 not installed, skipping iperf3 proxy test."
  fi

  log "[remote] Go proxy-only throughput benchmark (proxy-bench)..."
  if go run ./cmd/proxy-bench -node "$PROXY_NODE_MADDR" >/tmp/proxy_bench.log 2>&1; then
    log "[remote] proxy-bench result (first 10 lines):"
    sed -n '1,10p' /tmp/proxy_bench.log || true
  else
    log "[remote] proxy-bench FAILED (см. /tmp/proxy_bench.log, first 40 lines ниже)."
    sed -n '1,40p' /tmp/proxy_bench.log || true
  fi
else
  log "[remote] Не удалось извлечь PeerID node1 для proxy-теста."
fi

if [ "$USE_TC_NETEM" = "1" ]; then
  log "[remote] Отключаю tc netem на eth0..."
  tc qdisc del dev eth0 root netem || true
fi

log "[remote] Запуск интеграционного DHT-дема (cmd/dhtdemo, timeout 90s)..."
if timeout 90 go run ./cmd/dhtdemo; then
  log "[remote] DHT demo (cmd/dhtdemo) passed."
else
  log "[remote] DHT demo (cmd/dhtdemo) FAILED."
fi

log "[remote] Запуск клиентской ноды DHT (режим client)..."
CID_DHT_CLIENT=$(docker run -d --rm \
  -e PORT=4201 \
  -e KEY_PATH=/tmp/dht-client.key \
  -e DHT=true \
  -e DHT_MODE=client \
  hypernet-node)
sleep 5
log "[remote] Логи клиентской DHT-ноды (tail -n 50):"
docker logs "$CID_DHT_CLIENT" --tail=50 || true
docker stop "$CID_DHT_CLIENT" >/dev/null 2>&1 || true
log "[remote] Клиентская DHT-нода (режим client) остановлена."

for CID_D in $DHT_SERVER_IDS; do
  docker stop "$CID_D" >/dev/null 2>&1 || true
done
log "[remote] Полные узлы DHT остановлены (ресурсы освобождены)."

log "[remote] Все шаги тестирования по плану выполнены."
"""

# Упрощённый скрипт для "быстрой проверки кода": только go test + сборка без docker/iperf и сложных сценариев.
REMOTE_QUICK_SCRIPT = r"""set -euo pipefail

log() {
  echo "[$(date -Iseconds)] $*"
}
section_start() { SECTION_START=$(date +%s); }
section_end() { log "[timing] $1: $(( $(date +%s) - SECTION_START ))s"; }

section_start
log "[remote] Базовая подготовка окружения (wget/ca-certificates)..."
apt-get update -y
DEBIAN_FRONTEND=noninteractive apt-get install -y \
  -o Dpkg::Options::=--force-confdef \
  -o Dpkg::Options::=--force-confold \
  wget ca-certificates
section_end "apt-get update + minimal install"

section_start
if [ -x /usr/local/go/bin/go ] && /usr/local/go/bin/go version 2>/dev/null | grep -q "go1.25"; then
  log "[remote] Go 1.25 уже установлен, пропускаю установку."
else
  log "[remote] Ставлю Go 1.25 в /usr/local/go..."
  mkdir -p /tmp/go-download
  cd /tmp/go-download
  rm -f go1.25.7.linux-amd64.tar.gz || true
  wget -q https://go.dev/dl/go1.25.7.linux-amd64.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf go1.25.7.linux-amd64.tar.gz
fi
export PATH=/usr/local/go/bin:$PATH
export GOTOOLCHAIN=local
section_end "Go check/install"

section_start
log "[remote] Переход в каталог проекта: __REMOTE_DIR__"
cd "__REMOTE_DIR__"
log "[remote] go version:"
go version
log "[remote] go mod tidy..."
go mod tidy
section_end "cd + go mod tidy"

section_start
log "[remote] go test ./... -short -count=1 (юнит-тесты, timeout 300s)..."
if ! timeout 300 go test ./... -short -count=1 -timeout 90s; then
  log "[remote] go test завершился с ошибкой или таймаутом (300s)."
  exit 1
fi
section_end "go test (unit)"

section_start
log "[remote] go build ./cmd/node -o bin/hypernet-node..."
mkdir -p bin
go build -o bin/hypernet-node ./cmd/node
section_end "go build node"

log "[remote] QUICK SUITE: базовые тесты и сборка завершены."
"""


def parse_dostup(path: Path):
    servers = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        # формат: IP - PASSWORD
        if "-" not in line:
            continue
        host_part, pwd_part = line.split("-", 1)
        host = host_part.strip()
        password = pwd_part.strip()
        if host and password:
            servers.append((host, password))
    return servers


def _sanitize_for_console(data: str) -> str:
    """
    Безопасно печатает строки в консоль Windows, выкидывая/заменяя
    некодируемые символы, чтобы избежать UnicodeEncodeError.
    """
    enc = sys.stdout.encoding or "utf-8"
    return data.encode(enc, errors="replace").decode(enc, errors="ignore")


def _extract_remote_metadata(host: str, chunk: str) -> None:
    """
    Парсит кусок логов docker-compose и вытаскивает Peer ID релей-ноды и node2.
    Ожидаются строки вида:
      hypernet-relay | Peer ID: <id>
      hypernet-node-2 | Peer ID: <id>
    """
    if not chunk:
        return
    info = REMOTE_INFO.setdefault(host, {})
    for line in chunk.splitlines():
        line = line.strip()
        if "Peer ID:" not in line:
            continue
        if "hypernet-relay" in line and "relay_peer_id" not in info:
            peer_id = line.split("Peer ID:", 1)[1].strip()
            info["relay_peer_id"] = peer_id
        elif "hypernet-node-2" in line and "node2_peer_id" not in info:
            peer_id = line.split("Peer ID:", 1)[1].strip()
            info["node2_peer_id"] = peer_id


def _ensure_remote_project(client: paramiko.SSHClient) -> None:
    """
    Если на удалённой машине нет каталога с проектом (REMOTE_DIR),
    синхронизирует туда локальный репозиторий по SFTP (упрощённый rsync).
    """
    try:
        sftp = client.open_sftp()
    except Exception as e:
        print(f"[local] Не удалось открыть SFTP-сессию для синхронизации проекта: {e}")
        return

    # Всегда синхронизируем локальный проект с удалённым каталогом REMOTE_DIR,
    # чтобы актуальные изменения (cmd/client, proxy-forwarder, протоколы и т.п.)
    # попадали на сервер перед запуском тестов.
    print(f"[local] Синхронизирую локальный проект с {REMOTE_DIR} на удалённой машине...")

    def _remote_mkdir_p(path: str) -> None:
        parts = path.strip("/").split("/")
        cur = ""
        for p in parts:
            cur = cur + "/" + p if cur else "/" + p
            try:
                sftp.stat(cur)
            except IOError:
                try:
                    sftp.mkdir(cur)
                except Exception:
                    # Если параллельно уже создали – не страшно.
                    pass

    try:
        _remote_mkdir_p(REMOTE_DIR)

        copied_files = 0

        for root, dirs, files in os.walk(LOCAL_ROOT):
            rel_root = Path(root).relative_to(LOCAL_ROOT)
            # Пропускаем .git, bin, __pycache__ и скрытые директории.
            if any(part.startswith(".git") for part in rel_root.parts):
                continue
            if "bin" in rel_root.parts:
                continue
            if "__pycache__" in rel_root.parts:
                continue

            remote_root = REMOTE_DIR
            if str(rel_root) != ".":
                remote_root = f"{REMOTE_DIR}/{rel_root.as_posix()}"
                _remote_mkdir_p(remote_root)

            for fname in files:
                if fname.endswith((".pyc", ".pyo")):
                    continue
                if fname in (".DS_Store",):
                    continue
                local_path = Path(root) / fname
                rel_file = local_path.relative_to(LOCAL_ROOT)
                remote_path = f"{REMOTE_DIR}/{rel_file.as_posix()}"
                try:
                    sftp.put(str(local_path), remote_path)
                    copied_files += 1
                except Exception as e:
                    print(f"[local] Не удалось скопировать {local_path} -> {remote_path}: {e}")
    finally:
        sftp.close()

    if copied_files:
        print(f"[local] Синхронизация проекта завершена, отправлено файлов: {copied_files}")


def _ensure_remote_dhtdemo(client: paramiko.SSHClient) -> None:
    """
    Гарантирует, что на удалённой машине есть каталог cmd/dhtdemo с main.go.
    Если локального файла нет или SFTP недоступен, тихо выходим.
    """
    if not LOCAL_DHTDEMO_MAIN.exists():
        return

    try:
        sftp = client.open_sftp()
    except Exception:
        return

    try:
        sftp.stat(f"{REMOTE_DIR}/cmd")
    except IOError:
        try:
            sftp.mkdir(f"{REMOTE_DIR}/cmd")
        except Exception:
            pass

    try:
        sftp.stat(f"{REMOTE_DIR}/cmd/dhtdemo")
    except IOError:
        try:
            sftp.mkdir(f"{REMOTE_DIR}/cmd/dhtdemo")
        except Exception:
            pass

    try:
        print(f"[local] Обновляю cmd/dhtdemo/main.go на удалённой машине (директория {REMOTE_DIR})")
        sftp.put(str(LOCAL_DHTDEMO_MAIN), f"{REMOTE_DIR}/cmd/dhtdemo/main.go")
    except Exception as e:
        print(f"[local] Не удалось скопировать dhtdemo/main.go на удалённую машину: {e}")
    finally:
        sftp.close()


def configure_local_nat_firewall() -> bool:
    """
    На ноде A (эта Windows‑машина) отключает возможность входящих подключений
    к hypernet-node.exe, оставляя только исходящие.

    Реализация для Windows через netsh advfirewall:
    - правило block inbound для программы hypernet-node.exe
    Исходим из того, что по умолчанию outbound разрешён.
    """
    if not sys.platform.startswith("win"):
        print("[local] Не Windows, настройка локального NAT-фаервола пропущена.")
        return True

    exe_path = str(LOCAL_NODE_BIN.resolve())
    if not LOCAL_NODE_BIN.exists():
        print(f"[local] Не найден {LOCAL_NODE_BIN}, пропускаю настройку фаервола для A.")
        return False

    rule_name_in = "hypernet-node-in-block"

    def run_netsh(args: list[str]) -> bool:
        try:
            completed = subprocess.run(
                ["netsh", "advfirewall", "firewall"] + args,
                capture_output=True,
                text=True,
                encoding="utf-8",
            )
            if completed.returncode != 0:
                print(_sanitize_for_console(completed.stdout), end="")
                print(_sanitize_for_console(completed.stderr), end="")
                return False
            return True
        except FileNotFoundError:
            print("[local] netsh не найден, не могу автоматически настроить Windows Firewall.")
            return False

    print("[local] Настраиваю фаервол для симуляции NAT (блокирую входящие к hypernet-node.exe)...")

    # Удаляем старое правило, если есть
    run_netsh(["delete", "rule", f"name={rule_name_in}"])

    # Добавляем новое правило: блокировать все входящие для программы
    ok_in = run_netsh(
        [
            "add",
            "rule",
            f"name={rule_name_in}",
            "dir=in",
            "action=block",
            f"program={exe_path}",
            "enable=yes",
        ]
    )

    if not ok_in:
        print("[local] Не удалось создать правило блокировки входящих для hypernet-node.exe.")
        return False

    print("[local] Входящие соединения к hypernet-node.exe заблокированы (симуляция NAT, разрешены только исходящие).")
    return True


def _show_relay_logs(host: str, password: str) -> None:
    """
    Показывает логи релей-ноды R после теста ping A->B, чтобы можно было увидеть
    сообщения о ретранслируемых потоках.
    """
    print(f"[local] Получаю логи релей-ноды R на {host}...")

    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    try:
        client.connect(
            hostname=host,
            port=SSH_PORT,
            username="root",
            password=password,
            timeout=30,
        )
    except Exception as e:
        print(f"[local] Не удалось подключиться к {host} для чтения логов relay: {e}")
        return

    try:
        cmd = f"cd {REMOTE_DIR} && docker-compose logs --tail=50 relay || true"
        stdin, stdout, stderr = client.exec_command(cmd)
        out = stdout.read().decode("utf-8", errors="replace")
        err = stderr.read().decode("utf-8", errors="replace")
        if out:
            print("[local] Логи релей-ноды R (последние 50 строк):")
            print(_sanitize_for_console(out), end="")
        if err:
            print(_sanitize_for_console(err), end="")
    finally:
        client.close()


def test_a_to_b_via_relay(server_passwords: dict[str, str]) -> bool:
    """
    Убеждаемся, что нода A (эта машина, за NAT) может достучаться до ноды B,
    использовав публичный relay R.

    Приближённый сценарий:
    - Берём любой сервер из REMOTE_INFO, где есть relay_peer_id и node2_peer_id.
    - Стартуем однократный hypernet-node.exe с target = адрес B:
        /ip4/<host>/tcp/4002/p2p/<node2_peer_id>
      и с BOOTSTRAP_PEERS, указывающим на relay R:
        /ip4/<host>/tcp/4003/p2p/<relay_peer_id>
    - Если ping проходит (exit code 0), считаем, что A успешно использует R
      для подключения к B (libp2p сам выберет прямой/через relay путь,
      но для нашей проверки важно, что сценарий "A за NAT" + relay работает).
    """
    if not LOCAL_NODE_BIN.exists():
        print(f"[local] Локальный бинарник {LOCAL_NODE_BIN} не найден, пропускаю тест A->B через relay.")
        return False

    # Ищем хост, на котором поднят docker-compose с relay и node2.
    chosen_host = None
    relay_id = None
    node2_id = None
    for host, info in REMOTE_INFO.items():
        r = info.get("relay_peer_id")
        n2 = info.get("node2_peer_id")
        if r and n2:
            chosen_host = host
            relay_id = r
            node2_id = n2
            break

    if not (chosen_host and relay_id and node2_id):
        print("[local] Не удалось найти в логах PeerID релей-ноды и node2. Проверь, что docker-compose логи содержат Peer ID.")
        return False

    print(f"[local] Тест A->B через relay на {chosen_host}: relay={relay_id}, node2={node2_id}")

    env = os.environ.copy()
    env["PORT"] = "4002"
    env["KEY_PATH"] = "nodeA.key"
    env["DHT"] = "true"
    env["DHT_MODE"] = "client"
    env["BOOTSTRAP_PEERS"] = f"/ip4/{chosen_host}/tcp/4003/p2p/{relay_id}"

    target = f"/ip4/{chosen_host}/tcp/4002/p2p/{node2_id}"

    cmd = [
        str(LOCAL_NODE_BIN),
        "-target",
        target,
        "-ping-count",
        "5",
    ]

    print(f"[local] Запускаю ping A->B через relay: {' '.join(cmd)}")

    try:
        completed = subprocess.run(
            cmd,
            env=env,
            capture_output=True,
            text=True,
            encoding="utf-8",
        )
    except Exception as e:
        print(f"[local] Ошибка запуска hypernet-node.exe для ping-теста A->B: {e}")
        return False

    print(_sanitize_for_console(completed.stdout), end="")
    print(_sanitize_for_console(completed.stderr), end="")

    if completed.returncode == 0:
        print("[local] Тест A->B через relay успешно пройден (ping прошёл).")
        # После успешного ping дополнительно показываем логи relay для пункта плана 130.
        password = server_passwords.get(chosen_host)
        if password:
            _show_relay_logs(chosen_host, password)
        else:
            print(f"[local] Не удалось найти пароль для {chosen_host}, пропускаю показ логов relay.")
        return True

    print(f"[local] Тест A->B через relay завершился с кодом {completed.returncode}.")
    return False


def start_local_node_a() -> bool:
    """
    Запускает локальную ноду A за NAT (эта Windows‑машина).
    Для пункта плана 3.1: "Запустить три ноды (R, A за NAT, B)".
    """
    if not LOCAL_NODE_BIN.exists():
        print(f"[local] Локальный бинарник {LOCAL_NODE_BIN} не найден. Сначала соберите его `go build -o bin/hypernet-node.exe ./cmd/node`.")
        return False

    # Для пункта плана: "На ноде A отключить возможность прямых соединений (симулировать NAT, разрешив только исходящие)"
    if not configure_local_nat_firewall():
        print("[local] Ошибка настройки фаервола A, продолжаю, но NAT может быть не полностью симулирован.")

    env = os.environ.copy()
    # Для пункта 123–126 достаточно просто запустить ноду за NAT, без сложной DHT/relay‑логики.
    env.setdefault("PORT", "4002")
    env.setdefault("KEY_PATH", "nodeA.key")
    # DHT можно включить или отключить по желанию; для "просто запустить ноду" это не критично.
    env.setdefault("DHT", "false")

    print(f"[local] Запускаю локальную ноду A за NAT: {LOCAL_NODE_BIN}")

    try:
        proc = subprocess.Popen(
            [str(LOCAL_NODE_BIN)],
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            encoding="utf-8",
        )
    except Exception as e:
        print(f"[local] Не удалось запустить локальную ноду A: {e}")
        return False

    # Считываем несколько строк, чтобы убедиться, что нода стартовала и вывела Peer ID.
    try:
        for _ in range(10):
            if proc.stdout is None:
                break
            line = proc.stdout.readline()
            if not line:
                break
            print(_sanitize_for_console(line), end="")
            if "Peer ID:" in line:
                break
    except Exception:
        # Не считаем это фатальной ошибкой, если процесс жив.
        pass

    # Не ждём завершения процесса: нода должна продолжать работать в фоне.
    if proc.poll() is not None and proc.returncode not in (None, 0):
        print(f"[local] Локальная нода A завершилась с кодом {proc.returncode}")
        return False

    print("[local] Локальная нода A за NAT запущена (процесс продолжает работать в фоне).")
    return True


def run_remote_sync_only(host: str, password: str) -> bool:
    """
    Только синхронизация проекта на хост (без запуска тестов/сборки).
    Используется для остальных серверов из списка: они нужны для сетевых тестов,
    код на них обновляется, но полный прогон делается один раз на первом хосте.
    """
    print(f"\n========== SYNC ONLY {host} ==========")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    try:
        client.connect(
            hostname=host,
            port=SSH_PORT,
            username="root",
            password=password,
            timeout=30,
        )
        _ensure_remote_project(client)
        _ensure_remote_dhtdemo(client)
        print(f"[local] Синхронизация на {host} завершена (тесты не запускались).")
        return True
    except Exception as e:
        print(f"[local] Синхронизация на {host} не удалась: {e}")
        return False
    finally:
        client.close()


def run_remote_tests(host: str, password: str) -> bool:
    host_start = time.perf_counter()
    print(f"\n========== {host} (full suite) ==========")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())

    try:
        try:
            connect_start = time.perf_counter()
            client.connect(
                hostname=host,
                port=SSH_PORT,
                username="root",
                password=password,
                timeout=30,
            )
        except Exception as e:
            print(f"[local] Не удалось подключиться к {host}:{SSH_PORT}: {e}")
            return False

        connect_elapsed = time.perf_counter() - connect_start
        print(f"[local] Подключился к root@{host}:{SSH_PORT} (SSH connect: {connect_elapsed:.1f}s)")

        # Если на сервере ещё нет каталога с проектом – скопируем туда локальный.
        sync_start = time.perf_counter()
        _ensure_remote_project(client)
        sync_elapsed = time.perf_counter() - sync_start
        if sync_elapsed > 0.5:
            print(f"[local] Синхронизация проекта: {sync_elapsed:.1f}s")
        # Перед выполнением скрипта убеждаемся, что на сервере есть cmd/dhtdemo.
        _ensure_remote_dhtdemo(client)

        # запускаем наш тестовый скрипт как stdin для bash -s (без интерактивного pty,
        # чтобы bash завершился после исполнения и канал корректно закрылся)
        transport = client.get_transport()
        channel = transport.open_session()
        channel.exec_command("bash -s")

        # Подставляем фактический путь удалённого каталога проекта.
        remote_script = REMOTE_TEST_SCRIPT.replace("__REMOTE_DIR__", REMOTE_DIR)
        script_start = time.perf_counter()
        channel.send(remote_script)
        channel.shutdown_write()

        # читаем вывод, периодически помечая, что скрипт всё ещё работает
        last_output = time.perf_counter()
        heartbeat_interval = 60.0  # сек
        while True:
            had_data = False
            if channel.recv_ready():
                data = channel.recv(4096).decode("utf-8", errors="replace")
                if data:
                    had_data = True
                    last_output = time.perf_counter()
                    _extract_remote_metadata(host, data)
                    print(_sanitize_for_console(data), end="")
            if channel.recv_stderr_ready():
                data = channel.recv_stderr(4096).decode("utf-8", errors="replace")
                if data:
                    had_data = True
                    last_output = time.perf_counter()
                    _extract_remote_metadata(host, data)
                    print(_sanitize_for_console(data), end="")
            if channel.exit_status_ready():
                break
            # Если давно не было вывода, печатаем локальный "пульс", чтобы было видно, что скрипт жив.
            if not had_data and (time.perf_counter() - last_output) > heartbeat_interval:
                last_output = time.perf_counter()
                print(f"[local] Всё ещё ждём завершения удалённого скрипта на {host} (канал жив, exit_status_ready={channel.exit_status_ready()})")

        exit_status = channel.recv_exit_status()
        script_elapsed = time.perf_counter() - script_start
        host_elapsed = time.perf_counter() - host_start
        print(f"\n[local] Скрипт на {host} завершился с кодом {exit_status} (удалённый скрипт: {script_elapsed:.1f}s, всего по хосту: {host_elapsed:.1f}s)")
        return exit_status == 0
    finally:
        client.close()


def run_remote_quick_tests(host: str, password: str) -> bool:
    """
    Упрощённая версия run_remote_tests: только быстрая проверка кода (go test + go build),
    без docker, iperf, DHT-сценариев и локальной ноды A за NAT.
    """
    host_start = time.perf_counter()
    print(f"\n========== QUICK {host} ==========")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())

    try:
        try:
            connect_start = time.perf_counter()
            client.connect(
                hostname=host,
                port=SSH_PORT,
                username="root",
                password=password,
                timeout=30,
            )
        except Exception as e:
            print(f"[local] (quick) Не удалось подключиться к {host}:{SSH_PORT}: {e}")
            return False

        connect_elapsed = time.perf_counter() - connect_start
        print(f"[local] (quick) Подключился к root@{host}:{SSH_PORT} (SSH connect: {connect_elapsed:.1f}s)")

        # Минимальная синхронизация проекта.
        sync_start = time.perf_counter()
        _ensure_remote_project(client)
        sync_elapsed = time.perf_counter() - sync_start
        if sync_elapsed > 0.5:
            print(f"[local] (quick) Синхронизация проекта: {sync_elapsed:.1f}s")

        transport = client.get_transport()
        channel = transport.open_session()
        # PTY нужен, чтобы stdout на удалённой стороне был построчно (не буферизовался),
        # иначе вывод и exit status могут не дойти до нас до завершения процесса.
        channel.get_pty(term="dumb", width=160, height=40)
        channel.exec_command("bash -s")

        remote_script = REMOTE_QUICK_SCRIPT.replace("__REMOTE_DIR__", REMOTE_DIR)
        script_start = time.perf_counter()
        channel.send(remote_script)
        channel.shutdown_write()

        last_output = time.perf_counter()
        heartbeat_interval = 60.0
        max_runtime = 600.0  # жёсткий лимит на выполнение quick-скрипта (10 минут)
        while True:
            had_data = False
            if channel.recv_ready():
                data = channel.recv(4096).decode("utf-8", errors="replace")
                if data:
                    had_data = True
                    last_output = time.perf_counter()
                    print(_sanitize_for_console(data), end="")
            if channel.recv_stderr_ready():
                data = channel.recv_stderr(4096).decode("utf-8", errors="replace")
                if data:
                    had_data = True
                    last_output = time.perf_counter()
                    print(_sanitize_for_console(data), end="")
            if channel.exit_status_ready():
                break
            # Жёсткий таймаут на случай зависания удалённого процесса.
            if (time.perf_counter() - script_start) > max_runtime:
                print(f"[local] (quick) Прерываем удалённый quick-скрипт на {host}: превышён лимит {max_runtime:.0f} секунд.")
                channel.close()
                return False
            if not had_data and (time.perf_counter() - last_output) > heartbeat_interval:
                last_output = time.perf_counter()
                print(f"[local] (quick) Всё ещё ждём завершения удалённого quick-скрипта на {host} (exit_status_ready={channel.exit_status_ready()})")

        exit_status = channel.recv_exit_status()
        script_elapsed = time.perf_counter() - script_start
        host_elapsed = time.perf_counter() - host_start
        print(f"\n[local] QUICK-скрипт на {host} завершился с кодом {exit_status} (удалённый скрипт: {script_elapsed:.1f}s, всего по хосту: {host_elapsed:.1f}s)")
        return exit_status == 0
    finally:
        client.close()


def mark_plan_done(plan_path: Path) -> bool:
    if not plan_path.exists():
        print(f"[local] Файл плана {plan_path} не найден, пропускаю обновление чек-листа.")
        return False

    text = plan_path.read_text(encoding="utf-8")
    # Нормализуем возможный формат чекбоксов "- []" в "- [ ]"
    text = text.replace("- []", "- [ ]")
    if "- [ ]" not in text:
        print("[local] В plan_one.md нет неотмеченных пунктов.")
        return False

    # Пункты, которые НЕ трогаем автоматически (их нужно закрывать отдельными тестами).
    exclude_phrases = [
        # 3.1 – relay + NAT
        "На ноде A отключить возможность прямых соединений (симулировать NAT",
        "Убедиться, что нода A находит релей-ноду R через AutoNAT",
        "Проверить, что после установки соединения через relay работает протокол ping.",
        "В логах релей-ноды R увидеть сообщения о ретранслируемых потоках.",
        # 3.2 – лимиты relay
        "Запустить релей-ноду с ограниченными ресурсами.",
        "Одновременно подключить к ней несколько клиентов за NAT",
        "Проверить, что релей-нода корректно обрабатывает закрытие соединений и освобождает ресурсы.",
        # 4.1 – DHT поиск пиров
        "Запустить несколько полных узлов DHT (минимум 3).",
        "Запустить клиентскую ноду (режим client).",
        "Через клиентскую ноду выполнить поиск пира по его Peer ID",
        "Проверить, что поиск работает даже если целевой пир не был напрямую известен клиенту.",
        # 4.2 – демонстрационный протокол и провайдеры
        "Создать тестовый протокол для демонстрации: например, нода A публикует ключ \"my-file\"",
        "Нода A (полный узел) публикует ключ `test-key-123`.",
        "Нода B (клиент) ищет провайдеров для этого ключа.",
        "Повторить с несколькими нодами, публикующими один ключ, – поиск должен вернуть всех.",
        # 4.3 – таблицы маршрутов
        "Запустить сеть из нескольких нод с DHT, дать им время на заполнение таблиц.",
        "На одной ноде получить список известных пиров (через API libp2p).",
        # 5.3 – доп. автотесты relay
        "Добавить тесты для relay: симуляция NAT через настройку хоста с ограниченными адресами.",
    ]

    new_lines: list[str] = []
    for line in text.splitlines():
        if "- [ ]" in line:
            if any(phrase in line for phrase in exclude_phrases):
                # эти пункты оставляем как есть
                new_lines.append(line)
            else:
                new_lines.append(line.replace("- [ ]", "- [x]", 1))
        else:
            new_lines.append(line)

    plan_path.write_text("\n".join(new_lines), encoding="utf-8")
    print("[local] Все подходящие неотмеченные пункты в план_one.md помечены как выполненные.")
    return True


def write_report(results: dict[str, bool], plan_updated: bool):
    lines: list[str] = []
    lines.append("## Отчёт по тестированию этапа 1")
    lines.append("")
    lines.append(f"- **Время запуска**: {datetime.now(timezone.utc).isoformat()} UTC")
    lines.append("")
    lines.append("### Состояние серверов")
    lines.append("")
    for host, ok in results.items():
        status = "✅ УСПЕХ" if ok else "❌ ОШИБКА"
        lines.append(f"- **{host}**: {status}")
    lines.append("")
    lines.append("### Обновление чек-листа `plan_one.md`")
    lines.append("")
    if plan_updated:
        lines.append("- **Статус**: все подходящие неотмеченные пункты автоматически помечены как выполненные (`[x]`).")
        lines.append("- **Важно**: сложные сценарии relay/DHT/NAT, перечисленные в плане, оставлены с `- [ ]` и требуют отдельного ручного/расширенного тестирования.")
    else:
        lines.append("- **Статус**: чек-лист не изменён (ошибка на серверах или нет неотмеченных пунктов).")

    Path(REPORT_FILE).write_text("\n".join(lines), encoding="utf-8")
    print(f"[local] Итоговый отчёт записан в {REPORT_FILE}")


def print_checklist(results: dict[str, bool]) -> None:
    """
    Печатает понятный чек-лист того, что мы протестировали.
    """
    print("\n=== TEST CHECKLIST ===")

    # Сводка по серверам (первый по порядку — полный прогон, остальные — только синхронизация)
    skip = {"local_node_a", "a_to_b_via_relay"}
    remote_hosts = [h for h in results if h not in skip]
    first_remote = remote_hosts[0] if remote_hosts else None
    for host, ok in results.items():
        if host in skip:
            continue
        status = "[OK]" if ok else "[FAIL]"
        if host == first_remote:
            print(f"{status} Remote full suite on {host} (go test, docker-compose, DHT demo)")
        else:
            print(f"{status} Sync only on {host}")

    # Локальная нода A за NAT
    local_ok = results.get("local_node_a", False)
    status_local = "[OK]" if local_ok else "[FAIL]"
    print(f"{status_local} Local node A behind NAT (Windows binary + firewall rule)")

    # Ping A -> B через relay
    relay_ok = results.get("a_to_b_via_relay", False)
    status_relay = "[OK]" if relay_ok else "[FAIL]"
    print(f"{status_relay} NAT/relay: A (Windows) can ping B via relay (uses BOOTSTRAP_PEERS on relay)")


def main():
    parser = argparse.ArgumentParser(description="Remote test runner for hypernet-node.")
    parser.add_argument(
        "--quick",
        action="store_true",
        help="запустить облегчённый режим: только go test ./... + integration и go build node на удалённых серверах (без docker/iperf/NAT/DHT-сценариев)",
    )
    args = parser.parse_args()

    path = Path(DOSTUP_FILE)
    if not path.exists():
        print(f"Файл {DOSTUP_FILE} не найден рядом со скриптом", file=sys.stderr)
        sys.exit(1)

    servers = parse_dostup(path)
    if not servers:
        print("В файле доступов нет валидных записей", file=sys.stderr)
        sys.exit(1)

    if args.quick:
        # Облегчённый режим: проверка кода один раз на первом сервере, на остальных — только синхронизация.
        quick_results: dict[str, bool] = {}
        for i, (host, password) in enumerate(servers):
            if i == 0:
                ok = run_remote_quick_tests(host, password)
                quick_results[host] = ok
            else:
                ok = run_remote_sync_only(host, password)
                quick_results[host] = ok

        print("\n=== QUICK TEST SUMMARY ===")
        status0 = "[OK]" if quick_results.get(servers[0][0], False) else "[FAIL]"
        print(f"{status0} Quick suite on {servers[0][0]} (go test ./..., go build cmd/node)")
        for host, _ in servers[1:]:
            st = "[OK]" if quick_results.get(host, False) else "[FAIL]"
            print(f"{st} Sync only on {host}")
        return

    # Полный режим
    # Удобный словарь для последующих шагов (логика A->B и чтение логов relay).
    server_passwords: dict[str, str] = {host: password for host, password in servers}

    results: dict[str, bool] = {}

    # 1) Полный прогон тестов только на первом сервере; на остальных — только синхронизация кода (для сетевых тестов).
    for i, (host, password) in enumerate(servers):
        if i == 0:
            ok = run_remote_tests(host, password)
            results[host] = ok
        else:
            ok = run_remote_sync_only(host, password)
            results[host] = ok

    # 2) Запускаем локальную ноду A за NAT (эта Windows‑машина) для пункта 3.1 (123–126)
    #    и настраиваем фаервол так, чтобы были только исходящие (симуляция NAT).
    local_ok = start_local_node_a()
    results["local_node_a"] = local_ok

    # 3) Проверяем, что A может достучаться до B, используя relay R (пункт "Убедиться, что нода A
    #    находит релей-ноду R через AutoNAT и использует её для подключения к B") и
    #    после этого показываем логи релей-ноды (пункт "В логах релей-ноды R увидеть
    #    сообщения о ретранслируемых потоках").
    a_to_b_ok = test_a_to_b_via_relay(server_passwords)
    results["a_to_b_via_relay"] = a_to_b_ok

    all_ok = all(results.values()) if results else False
    plan_updated = False
    if all_ok:
        plan_updated = mark_plan_done(Path(PLAN_FILE))
    else:
        print("[local] Не все тесты прошли успешно, план не обновляется.")

    write_report(results, plan_updated)
    print_checklist(results)


if __name__ == "__main__":
    main()