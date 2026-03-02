## Hypernet Node — слоистая архитектура

Этот репозиторий содержит реализацию P2P‑ноды Hypernet — «внутреннего интернета» поверх libp2p с прокси‑сервисами, DHT‑DNS, VPN/exit‑слоем и зачатком экономического слоя.
Код организован по слоям:

- **core** — чистое сетевое ядро (идентичность, транспорты, обфускация, DHT, маршрутизация).
- **services** — сервисы поверх сети (proxy, DNS, relay, VPN).
- **economy** — границы работы с блокчейном и подписками.
- **node** — композиция ноды и роли.
- **apps** — прикладные CLI.
- **cmd** — тонкие `main`‑обёртки, используемые как бинарные entrypoint’ы.
- **pkg** — существующие вспомогательные пакеты (клиент, протокол‑адаптеры и т.п.), которые ещё не полностью перенесены в `hypernet/`.

Ниже — обзор ключевых директорий и основных файлов.

---

## Верхнеуровневые entrypoint’ы (`cmd/`)

- **`cmd/node/main.go`**: точка входа ноды.
  - Загружает конфиг (`pkg/config`), настраивает context и обработку сигналов.
  - Опционально включает pprof.
  - Делегирует основную работу в `hypernet/node/daemon.Run`.
- **`cmd/node/main_test.go`**: smoke‑тест, который запускает `daemon.Run` с минимальным конфигом и проверяет, что нода корректно стартует и останавливается.
- **`cmd/client/main.go`**: тонкий враппер над клиентским CLI, вызывает `hypernet/apps/cli.Main`.
- **`cmd/proxy-bench/main.go`**, **`cmd/proxy-forwarder/main.go`**, **`cmd/proxy-load/main.go`**, **`cmd/longrun-tester/main.go`**, **`cmd/dhtdemo/main.go`**:
  вспомогательные утилиты и демо (нагрузочные тесты прокси, длительный прогон, демо‑работа с DHT и т.д.).

---

## Ядро сети (`hypernet/core`)

- **`hypernet/core/identity`**
  - `identity.go`: загрузка/создание приватного ключа ноды и обёртка над libp2p‑identity.
  - `identity_test.go`: тесты корректности создания/загрузки ключей.
  - `doc.go`: краткое описание пакета.

- **`hypernet/core/network/node`**
  - `node.go`: создание `Node` (обёртка над libp2p `host.Host` и DHT):
    - выбор слушающих адресов (TCP/QUIC),
    - включение relay v2 и AutoRelay (по `config.Config`),
    - инициализация и bootstrap DHT с префиксом `/hypernet`,
    - подключение к bootstrap‑пирам.
  - `node_test.go`, `quic_integration_test.go`, `dht_helpers*_test.go`: юнит‑ и интеграционные тесты поведения хоста и DHT.
  - `dht_helpers.go`: вспомогательные функции для работы с DHT в тестах.

- **`hypernet/core/network/transport`**
  - `transport.go`: общий интерфейс и регистрация транспортов.
  - `tcp.go`, `kcp.go`, `grpc.go`, `ws.go`, `unix.go`: конкретные реализации транспорта для разных протоколов/типов соединений.
  - `transport_test.go`: тесты корректности работы транспорта.

- **`hypernet/core/network/obfuscate`**
  - `utls.go`: клиентская обфускация TLS (uTLS‑dialer для маскировки под реальные браузеры).
  - `padding.go`: добавление паддинга к трафику.
  - `delay.go`: искусственные задержки записи для сглаживания паттернов.
  - `*_test.go`: юнит‑тесты obfuscation‑слоя.

- **`hypernet/core/network/ping`**
  - `ping.go`: протокол ping поверх libp2p, включая `PingTarget`.
  - `ping_integration_test.go`: интеграционные тесты ping между узлами.

- **`hypernet/core/routing`**
  - `routing.go`: интерфейсы/обёртки для DHT‑маршрутизации и discovery, чтобы верхние слои не зависели напрямую от конкретной реализации DHT.

- **`hypernet/core/network/doc.go`**: высокоуровневое описание сетевого ядра.

---

## Сервисы (`hypernet/services`)

### Proxy (`hypernet/services/proxy`)

- **`server/proxy.go`**:
  - Сервер прокси‑протоколов (VLESS, VMess, Trojan, Shadowsocks, Hysteria2, plain).
  - Регистрация хэндлеров для libp2p‑host, учёт метрик качества и сессий.
  - Поддержка обфускации (padding + delay) на серверной стороне.
- **`server/obfs_negotiate.go`**:
  - Переговоры по параметрам обфускации между клиентом и сервером.
- **`server/proxy_test.go`, `server/proxy_server_test.go`**:
  - Тесты протокольного сервера и сценариев работы.

- **`protoiface/protoiface.go`**:
  - Общие интерфейсы для протоколов (прокси‑адаптеров), сессий и feature‑флагов.
  - Используются и сервером, и клиентскими адаптерами.

- **`protomanager/manager.go`**:
  - Управление списком поддерживаемых протоколов и их приоритетами.
  - Предоставляет `SupportedNames()` и выбор протокола по результатам negotiation.
  - `manager_test.go`: тесты порядка и выбора протоколов.

- **`protonegotiate/negotiate.go`**:
  - Протокол согласования (negotiation) между двумя пиарами:
    обмен списками поддерживаемых протоколов и выбор общего.
  - `negotiate_test.go`: тесты успешного/ошибочного negotiation.

- **`metrics/store.go`**:
  - SQLite‑хранилище сессий и quality‑метрик (RTT, ошибки, трафик).
  - Асинхронный flush, настройки интервалов.
  - `store_test.go`: тесты на корректность записи/чтения.

- **`inbound`**:
  - `outbound.go`: интерфейс `Outbound` для исходящих соединений из inbound‑слоя.
  - `freedom.go`: реализация прямого выхода в интернет.
  - `blackhole.go`: «чёрная дыра» (дропаут трафика).
  - `redirect.go`: редирект трафика на заданный `host:port` (используется Dokodemo‑door).
  - `socks5.go`: SOCKS5‑сервер, использующий `Outbound`.
  - `http_proxy.go`: HTTP CONNECT‑прокси.
  - `dokodemo.go`: Dokodemo‑door реализация.
  - Логика включается из `hypernet/node/daemon.Run` в зависимости от конфига.

- **`smartclient/smartclient.go`**:
  - DHT‑aware клиент, который:
    - использует DHT/DNS‑информацию для выбора подходящих нод‑провайдеров,
    - управляет кэшем провайдеров,
    - умеет fallback/переключение протоколов.
  - `smartclient_integration_test.go`: интеграционные тесты поведения smart‑клиента.

- **`doc.go`**: описание proxy‑сервиса как слоя поверх `core/network`.

### DNS (`hypernet/services/dns`)

- `namesystem.go`:
  - DHT‑базированная система имён: регистрация и разрешение человекочитаемых имён на multiaddr.
- `record.go`:
  - Формат и подпись DNS‑записей, проверка подписи.
- `dht_validator.go`:
  - Валидатор записей в DHT под префиксом `/hypernet/name/`.
- `doc.go`: краткое описание DHT‑DNS как сервиса.

### Relay (`hypernet/services/relay`)

- `config.go`:
  - Узкий тип `relay.Config` с полями:
    `Enable`, `MaxConnections`, `DataLimitBytes`, `BootstrapPeers`.
  - Функция `FromNodeConfig` вытаскивает только relay‑часть из общего `config.Config`.
- `doc.go`: объяснение, что relay‑логика строится поверх `core/network/node`, а сам пакет представляет сервисный view.

### VPN / REALITY (`hypernet/services/vpn`)

- `doc.go`: описание VPN/exit‑слоя как набора сервисов поверх прокси.
- `reality/server.go`:
  - Интеграция с `github.com/xtls/reality`:
    - `ServerConfig`, `BuildRealityConfig`, `Listener`, `ListenREALITY`.
    - Опциональная маскировка трафика под реальный HTTPS.
  - `ClientConfig`, `DialREALITY` — заглушка для будущего клиентского стека (пока возвращает ошибку).
- `reality/server_test.go`: тесты валидации конфигурации и поведения заглушки клиента.

---

## Экономический слой (`hypernet/economy`)

- **`blockchain_client`**
  - `doc.go`: описание роли пакета как границы взаимодействия с Hypernet‑блокчейном.
  - `interfaces.go`:
    - `type Network string` — логический идентификатор сети (mainnet/testnet).
    - `SubscriptionStatus` — нормализованный view статуса подписки (active/expiry/plan).
    - `Client` — минимальный интерфейс для чтения статуса подписки из блокчейна.
    - `Factory` — фабрика клиентов по сети и endpoint’у.
  - На данный момент нигде не используется, служит чистой границей; реализация будет добавлена позже.

- **`subscription`**
  - `doc.go`: пакет‑заглушка, в котором позже появится бизнес‑логика проверки подписки на основе `blockchain_client`.

- **`domain`**
  - `doc.go`: placeholder для будущих доменных моделей экономики (баланс, вознаграждения, тарифы и т.п.).

---

## Нода и роли (`hypernet/node`)

- **`daemon`**
  - `doc.go`: описание пакета как «composition root» ноды.
  - `run.go`:
    - Функция `Run(ctx context.Context, cfg *config.Config) error` — основной цикл ноды:
      - создаёт `core/network/node.Node`,
      - регистрирует ping‑протокол,
      - инициализирует `metrics.Store` и GeoIP,
      - настраивает и регистрирует proxy‑сервер,
      - публикует поддерживаемые протоколы в DHT,
      - настраивает и периодически обновляет DHT‑DNS‑записи,
      - регистрирует stream‑handler для `protonegotiate`,
      - поднимает inbound‑сервера (SOCKS5/HTTP‑proxy/Dokodemo),
      - при необходимости поднимает REALITY‑listener,
      - печатает PeerID и listen‑адреса,
      - выполняет ping‑клиента по `cfg.Target` либо ждёт завершения `ctx`.

- **`roles`**
  - `doc.go`: описание целевого назначения пакета.
  - `roles.go`:
    - `type Role string` и константы:
      - `RoleRelay` — нода‑relay,
      - `RoleExit` — нода‑exit/VPN,
      - `RoleOrigin` — origin/приложенческая нода.
    - Пока используются как задел для будущей конфигурации ролей.

---

## Прикладные приложения (`hypernet/apps`)

- **`hypernet/apps/cli`**
  - `doc.go`: назначение CLI‑слоя.
  - `client.go`:
    - Функция `Main()` реализует логику бинаря `hypernet-client`:
      - подкоманда `resolve <name>`: поднять light‑ноду, опросить DHT‑DNS и вывести multiaddr.
      - основной режим: подключиться к удалённой proxy‑ноде (адрес или DHT‑имя), установить TCP/UDP‑туннель и прокинуть stdin/stdout.
    - `clientConfigFromEnv`: строит минимальный конфиг клиента по env (bootstrap‑ноды и т.п.).
    - `runResolve`: вспомогательная функция для DHT‑DNS‑резолва имён.

---

## Существующие пакеты (`pkg/`)

Исторически код ноды находился под `pkg/`. Постепенно он выносится в `hypernet/`, но ряд пакетов остаётся и переиспользуется.

**pkg is deprecated and will be removed.**

- **`pkg/config/config.go`**:
  - Описание `Config` ноды и загрузка настроек:
    - `.env`, переменные окружения и флаги.
    - Параметры DHT, relay, DNS, inbound‑слоя, метрик, GeoIP, REALITY и очередности протоколов прокси.

- **`pkg/client/proxy.go`**:
  - Клиентская сторона прокси:
    - `ProxyClient` поверх libp2p‑host,
    - выбор и fallback протокола,
    - поддержка uTLS и интеграция с `smartclient`.
  - Интеграционные тесты в `proxy_fallback_integration_test.go`.

- **`pkg/protoadapters/*`**:
  - Адаптеры для различных прикладных протоколов:
    - `plain`, `vless`, `vmess`, `trojan`, `shadowsocks`, `hysteria2`.
  - Каждый пакет реализует интерфейсы из `hypernet/services/proxy/protoiface`.
  - Тесты протоколов и интеграции (`*_test.go`, `*_integration_test.go`).

- **`pkg/inbound`, `pkg/dns`, `pkg/reality`**:
  - Содержат исходные реализации, откуда логика была перенесена в `hypernet/services/...`.
  - Служат в основном для совместимости тестов и постепенной миграции.

---

## Как собирать и запускать

- **Сборка ноды**:
  - `go build ./cmd/node`
- **Сборка клиента**:
  - `go build ./cmd/client`
- **Запуск тестов**:
  - `go test ./...`

Конфигурация ноды управляется через переменные окружения и флаги, описанные в `pkg/config/config.go`. Рекомендуемый путь входа в код — с `cmd/node/main.go` (демон) и `hypernet/node/daemon/run.go` (композиция ноды и сервисов).
