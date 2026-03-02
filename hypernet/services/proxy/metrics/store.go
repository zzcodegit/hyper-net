package metrics

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SessionStart описывает минимальный набор данных о начале сессии.
type SessionStart struct {
	ID             string
	EntryNodePeer  string
	Protocol       string
	Transport      string
	ClientRegion   string
	NodeRegion     string
	StartedAt      time.Time
}

// SessionEnd описывает завершение сессии и итоговые счётчики.
type SessionEnd struct {
	ID          string
	BytesUp     int64
	BytesDown   int64
	Success     bool
	ErrorReason string
	EndedAt     time.Time
}

type op interface {
	apply(tx *sql.Tx) error
}

type opStart struct {
	s SessionStart
}

func (o opStart) apply(tx *sql.Tx) error {
	_, err := tx.Exec(`
INSERT INTO sessions (
  id, entry_node_peer_id, protocol, transport,
  client_region, node_region,
  start_time
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING
`, o.s.ID, o.s.EntryNodePeer, o.s.Protocol, o.s.Transport,
		o.s.ClientRegion, o.s.NodeRegion, o.s.StartedAt.Unix())
	return err
}

type opEnd struct {
	e SessionEnd
}

func (o opEnd) apply(tx *sql.Tx) error {
	_, err := tx.Exec(`
UPDATE sessions
SET end_time = ?, bytes_up = ?, bytes_down = ?, success = ?, error_reason = ?
WHERE id = ?
`, o.e.EndedAt.Unix(), o.e.BytesUp, o.e.BytesDown, o.e.Success, o.e.ErrorReason, o.e.ID)
	return err
}

type opUpdate struct {
	id        string
	deltaUp   int64
	deltaDown int64
}

func (o opUpdate) apply(tx *sql.Tx) error {
	_, err := tx.Exec(`
UPDATE sessions
SET bytes_up = COALESCE(bytes_up, 0) + ?, bytes_down = COALESCE(bytes_down, 0) + ?
WHERE id = ?
`, o.deltaUp, o.deltaDown, o.id)
	return err
}

type opQualitySample struct {
	sessionID string
	at        time.Time
	rttMS     int64
	jitterMS  int64
	lossPct   float64
}

func (o opQualitySample) apply(tx *sql.Tx) error {
	_, err := tx.Exec(`
INSERT INTO quality_samples (session_id, timestamp, rtt_ms, jitter_ms, loss_percent)
VALUES (?, ?, ?, ?, ?)
`, o.sessionID, o.at.Unix(), o.rttMS, o.jitterMS, o.lossPct)
	return err
}

type opEvent struct {
	sessionID string
	at        time.Time
	eventType string
	details   string
}

func (o opEvent) apply(tx *sql.Tx) error {
	_, err := tx.Exec(`
INSERT INTO events (session_id, timestamp, event_type, details)
VALUES (?, ?, ?, ?)
`, o.sessionID, o.at.Unix(), o.eventType, o.details)
	return err
}

// Store инкапсулирует SQLite‑хранилище и горутину‑писатель.
type Store struct {
	db           *sql.DB
	ops          chan op
	wg           sync.WaitGroup
	once         sync.Once
	flushInterval time.Duration
}

// NewStore открывает (или создаёт) базу данных и запускает горутину‑писатель.
// flushInterval задаёт максимальную задержку перед сбросом накопленных операций
// в базу (batched‑режим). При значении <= 0 используется интервал 200 мс.
func NewStore(path string, flushInterval time.Duration) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("metrics: empty db path")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("metrics: open db: %w", err)
	}
	// In-memory SQLite: one connection so that writer goroutine and callers see the same schema.
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if flushInterval <= 0 {
		flushInterval = 200 * time.Millisecond
	}

	s := &Store{
		db:            db,
		ops:           make(chan op, 1024),
		flushInterval: flushInterval,
	}
	s.wg.Add(1)
	go s.writerLoop()
	return s, nil
}

func initSchema(db *sql.DB) error {
	// Включаем WAL для лучшей конкурентности. В Docker/ограниченных средах WAL может не поддерживаться — тогда работаем без него.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		// Не падаем: в контейнерах часто "out of memory" / CANTOPEN при создании WAL-файлов.
		_, _ = db.Exec(`PRAGMA journal_mode=DELETE;`)
	}

	const createSessions = `
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  client_token TEXT,
  entry_node_peer_id TEXT,
  exit_node_peer_id TEXT,
  protocol TEXT,
  transport TEXT,
  client_region TEXT,
  node_region TEXT,
  start_time INTEGER,
  end_time INTEGER,
  bytes_up INTEGER,
  bytes_down INTEGER,
  success BOOLEAN,
  error_reason TEXT,
  handshake_ms INTEGER,
  rtt_avg INTEGER
);`

	if _, err := db.Exec(createSessions); err != nil {
		return fmt.Errorf("metrics: create sessions: %w", err)
	}

	const createQualitySamples = `
CREATE TABLE IF NOT EXISTS quality_samples (
  session_id TEXT,
  timestamp INTEGER,
  rtt_ms INTEGER,
  jitter_ms INTEGER,
  loss_percent REAL,
  FOREIGN KEY(session_id) REFERENCES sessions(id)
);`

	if _, err := db.Exec(createQualitySamples); err != nil {
		return fmt.Errorf("metrics: create quality_samples: %w", err)
	}

	const createEvents = `
CREATE TABLE IF NOT EXISTS events (
  session_id TEXT,
  timestamp INTEGER,
  event_type TEXT,
  details TEXT,
  FOREIGN KEY(session_id) REFERENCES sessions(id)
);`

	if _, err := db.Exec(createEvents); err != nil {
		return fmt.Errorf("metrics: create events: %w", err)
	}

	return nil
}

func (s *Store) writerLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	var batch []op
	flush := func() {
		if len(batch) == 0 {
			return
		}
		tx, err := s.db.Begin()
		if err != nil {
			batch = nil
			return
		}
		for _, o := range batch {
			_ = o.apply(tx)
		}
		_ = tx.Commit()
		batch = nil
	}

	for {
		select {
		case o, ok := <-s.ops:
			if !ok {
				flush()
				return
			}
			batch = append(batch, o)
			// Если накопили достаточно операций, сбрасываем досрочно.
			if len(batch) >= 128 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// opFunc — вспомогательный тип для вспомогательных операций (например, FlushNow).
type opFunc func(tx *sql.Tx) error

func (f opFunc) apply(tx *sql.Tx) error { return f(tx) }

// StartSession ставит в очередь запись о начале сессии.
func (s *Store) StartSession(start SessionStart) {
	if s == nil {
		return
	}
	select {
	case s.ops <- opStart{s: start}:
	default:
		// Если очередь переполнена, просто дропаем метрику, чтобы не тормозить /proxy.
	}
}

// EndSession ставит в очередь обновление о завершении сессии.
func (s *Store) EndSession(end SessionEnd) {
	if s == nil {
		return
	}
	select {
	case s.ops <- opEnd{e: end}:
	default:
	}
}

// UpdateSession инкрементально увеличивает счётчики bytes_up/bytes_down для сессии.
// Используется, если необходимо обновлять объём трафика до завершения сессии.
func (s *Store) UpdateSession(id string, deltaUp, deltaDown int64) {
	if s == nil {
		return
	}
	if deltaUp == 0 && deltaDown == 0 {
		return
	}
	select {
	case s.ops <- opUpdate{id: id, deltaUp: deltaUp, deltaDown: deltaDown}:
	default:
	}
}

// AddQualitySample добавляет замер качества для указанной сессии.
// rttMS/jitterMS указываются в миллисекундах, lossPct — в процентах (0.0–100.0).
func (s *Store) AddQualitySample(sessionID string, at time.Time, rttMS, jitterMS int64, lossPct float64) {
	if s == nil || sessionID == "" {
		return
	}
	select {
	case s.ops <- opQualitySample{
		sessionID: sessionID,
		at:        at,
		rttMS:     rttMS,
		jitterMS:  jitterMS,
		lossPct:   lossPct,
	}:
	default:
	}
}

// AddEvent добавляет событие для указанной сессии. details маршалится в JSON.
func (s *Store) AddEvent(sessionID string, eventType string, at time.Time, details any) {
	if s == nil || sessionID == "" || eventType == "" {
		return
	}
	var detailsJSON string
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			detailsJSON = string(b)
		}
	}
	select {
	case s.ops <- opEvent{
		sessionID: sessionID,
		at:        at,
		eventType: eventType,
		details:   detailsJSON,
	}:
	default:
	}
}

// FlushNow синхронно дожидается применения всех операций, записанных до вызова.
// Используется в тестах для детерминированной проверки состояния.
func (s *Store) FlushNow() {
	if s == nil {
		return
	}
	done := make(chan struct{})
	select {
	case s.ops <- opFunc(func(tx *sql.Tx) error {
		close(done)
		return nil
	}):
	default:
		// если очередь заполнена, просто ждём периодического flush по таймеру
		return
	}
	<-done
}

// Close останавливает горутину‑писатель и закрывает базу.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		close(s.ops)
	})
	s.wg.Wait()
	return s.db.Close()
}

