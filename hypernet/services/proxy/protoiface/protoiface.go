package protoiface

import (
	"io"
	"net"
	"time"
)

// Protocol описывает адаптер конкретного протокола (VLESS, Trojan, SS, Hysteria2 и т.д.).
// Он инкапсулирует рукопожатие, шифрование, обфускацию и работу поверх конкретного транспорта.
type Protocol interface {
	// Name возвращает машинно-читаемое имя протокола, например "vless", "trojan".
	Name() string

	// Handshake выполняет рукопожатие поверх уже установленного потока (libp2p stream).
	// На выходе возвращается сессия, через которую далее ходит пользовательский трафик.
	Handshake(stream io.ReadWriteCloser, cfg *Config) (Session, error)

	// Wrap оборачивает уже существующее соединение (TCP, QUIC stream и т.п.).
	// Используется, когда транспорт создан заранее.
	Wrap(conn net.Conn, cfg *Config) (io.ReadWriteCloser, error)

	// Features сообщает о возможностях конкретного протокола (транспорт, шифрование и т.п.).
	Features() Features
}

// Session представляет установленную сессию конкретного протокола.
// На первом этапе это просто io.ReadWriteCloser; при необходимости можно расширить.
type Session interface {
	io.ReadWriteCloser
}

// Features описывает возможности адаптера.
type Features struct {
	Transports   []string // "tcp", "tls", "ws", "quic", ...
	Mux          bool
	Encryption   string   // "aes-256-gcm", "chacha20-poly1305", ...
	Obfuscation  bool
	Description  string   // человеко-читаемое описание (для отладки/метаданных)
	Experimental bool     // пометка для экспериментальных протоколов
}

// Config содержит параметры, необходимые адаптерам при рукопожатии.
// Конкретные протоколы могут читать только нужные им поля.
type Config struct {
	// Token для аутентификации клиента/пользователя.
	Token string

	// Timeout для рукопожатия / установки сессии.
	Timeout time.Duration

	// Transport — идентификатор транспорта ("tcp", "ws", "grpc", "kcp").
	// Используется адаптерами и менеджером протоколов для выбора способа установки соединения.
	Transport string

	// Obfuscation: случайный паддинг к пакетам [PaddingMin, PaddingMax] байт (0 = отключено).
	PaddingMin, PaddingMax int
	// Задержка перед каждой записью [WriteDelayMin, WriteDelayMax] (0 = отключено).
	WriteDelayMin, WriteDelayMax time.Duration
	// UseUTLS — использовать uTLS с случайным fingerprint при исходящих TLS-соединениях.
	UseUTLS bool

	// Raw содержит любые дополнительные параметры протокола в виде
	// произвольной карты. Это упрощает эволюцию без ломающих изменений API.
	Raw map[string]any
}

