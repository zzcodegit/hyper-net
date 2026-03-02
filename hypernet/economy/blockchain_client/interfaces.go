package blockchain_client

import "context"

// Network describes a logical blockchain network (e.g. mainnet, testnet).
// It is intentionally minimal and string-based to avoid pulling specific SDK types.
type Network string

// SubscriptionStatus represents a normalized view of a user's subscription on chain.
type SubscriptionStatus struct {
	// Active показывает, есть ли действующая подписка.
	Active bool
	// ExpiresAtHeight — высота блока, до которой подписка считается действующей.
	// Ноль означает "неизвестно" или "нет ограничения по высоте" в текущей реализации.
	ExpiresAtHeight int64
	// Plan — произвольный идентификатор тарифного плана (месяц/год и т.п.).
	Plan string
}

// Client описывает минимальный интерфейс для чтения состояния подписки из блокчейна Hypernet.
// Конкретная реализация (Cosmos SDK, gRPC, REST и т.п.) будет добавлена позже.
type Client interface {
	// Network возвращает идентификатор сети (например, "hypernet-mainnet", "hypernet-testnet").
	Network() Network

	// SubscriptionStatusByAddress запрашивает статус подписки для заданного адреса.
	// addr — строковое представление адреса (bech32, hex и т.п. в зависимости от реализации).
	SubscriptionStatusByAddress(ctx context.Context, addr string) (SubscriptionStatus, error)
}

// Factory отвечает за создание клиентских инстансов для заданной сети/endpoint.
// Это позволит отделить конфигурацию (RPC/GRPC endpoints, время ожидания, ключи)
// от кода ноды и сервисов.
type Factory interface {
	// NewClient создаёт клиент для заданной сети и endpoint'а.
	// endpoint может быть URL RPC/GRPC или любая другая строка конфигурации.
	NewClient(network Network, endpoint string) (Client, error)
}

