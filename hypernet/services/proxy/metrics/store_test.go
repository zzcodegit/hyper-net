package metrics

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// waitUntil крутится до timeout, пока predicate не вернёт true.
func waitUntil(t *testing.T, timeout time.Duration, predicate func() bool) {
	deadline := time.Now().Add(timeout)
	for {
		if predicate() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not satisfied within %s", timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStore_StartAndEndSession(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "metrics.db")

	s, err := NewStore(dbPath, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer s.Close()

	id := "test-session-1"
	start := SessionStart{
		ID:            id,
		EntryNodePeer: "peer-entry",
		Protocol:      "plain",
		Transport:     "/proxy/1.0.0",
		ClientRegion:  "XX",
		NodeRegion:    "YY",
		StartedAt:     time.Unix(1000, 0),
	}
	s.StartSession(start)

	end := SessionEnd{
		ID:        id,
		BytesUp:   1234,
		BytesDown: 5678,
		Success:   true,
		EndedAt:   time.Unix(2000, 0),
	}
	s.EndSession(end)

	// Добавим один quality sample и одно событие.
	s.AddQualitySample(id, time.Unix(1500, 0), 25, 5, 0.1)
	s.AddEvent(id, "test_event", time.Unix(1500, 0), map[string]any{"k": "v"})

	// Дадим горутине-писателю немного времени на применение операций.
	time.Sleep(200 * time.Millisecond)

	// Проверяем, что итоговые значения bytes_up/bytes_down записаны корректно.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	defer db.Close()

	var up, down int64
	row := db.QueryRow(`SELECT bytes_up, bytes_down FROM sessions WHERE id = ?`, id)
	if err := row.Scan(&up, &down); err != nil {
		t.Fatalf("scan sessions row failed: %v", err)
	}

	// Проверяем, что хотя бы одна запись в quality_samples для этой сессии существует.
	var count int
	row2 := db.QueryRow(`SELECT COUNT(*) FROM quality_samples WHERE session_id = ?`, id)
	if err := row2.Scan(&count); err != nil {
		t.Fatalf("scan quality_samples count failed: %v", err)
	}
	if count == 0 {
		t.Fatalf("expected at least 1 quality_samples row for session %q", id)
	}

	// И что хотя бы одно событие в events тоже записано.
	var evCount int
	row3 := db.QueryRow(`SELECT COUNT(*) FROM events WHERE session_id = ?`, id)
	if err := row3.Scan(&evCount); err != nil {
		t.Fatalf("scan events count failed: %v", err)
	}
	if evCount == 0 {
		t.Fatalf("expected at least 1 events row for session %q", id)
	}

	if up != end.BytesUp || down != end.BytesDown {
		t.Fatalf("unexpected bytes_up/down: got (%d,%d), want (%d,%d)", up, down, end.BytesUp, end.BytesDown)
	}
}

// TestStore_InMemoryAndSchema проверяет, что при использовании :memory:
// создаются все необходимые таблицы.
func TestStore_InMemoryAndSchema(t *testing.T) {
	s, err := NewStore(":memory:", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewStore(:memory:) failed: %v", err)
	}
	defer s.Close()

	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("query sqlite_master failed: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name failed: %v", err)
		}
		found[name] = true
	}

	for _, tbl := range []string{"sessions", "quality_samples", "events"} {
		if !found[tbl] {
			t.Fatalf("expected table %q to exist in in-memory DB", tbl)
		}
	}
}

// TestStore_AsyncFlushAndData проверяет асинхронную запись, UpdateSession и корректность данных.
func TestStore_AsyncFlushAndData(t *testing.T) {
	s, err := NewStore(":memory:", 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewStore(:memory:) failed: %v", err)
	}
	defer s.Close()

	const id = "sess-async-1"
	s.StartSession(SessionStart{
		ID:        id,
		Protocol:  "plain",
		Transport: "/proxy/1.0.0",
		StartedAt: time.Unix(1000, 0),
	})

	// Несколько инкрементальных апдейтов.
	s.UpdateSession(id, 10, 20)
	s.UpdateSession(id, 5, 5)

	// Принудительно дожидаемся применения всех записей.
	s.FlushNow()

	var up, down int64
	row := s.db.QueryRow(`SELECT bytes_up, bytes_down FROM sessions WHERE id = ?`, id)
	if err := row.Scan(&up, &down); err != nil {
		t.Fatalf("scan after UpdateSession failed: %v", err)
	}
	if up != 15 || down != 25 {
		t.Fatalf("unexpected intermediate bytes_up/down: got (%d,%d), want (15,25)", up, down)
	}

	// Финальное завершение сессии должно перезаписать счётчики.
	s.EndSession(SessionEnd{
		ID:        id,
		BytesUp:   100,
		BytesDown: 200,
		Success:   true,
		EndedAt:   time.Unix(2000, 0),
	})

	// Ещё раз дожидаемся применения всех операций.
	s.FlushNow()

	row = s.db.QueryRow(`SELECT bytes_up, bytes_down FROM sessions WHERE id = ?`, id)
	if err := row.Scan(&up, &down); err != nil {
		t.Fatalf("scan after EndSession failed: %v", err)
	}
	if up != 100 || down != 200 {
		t.Fatalf("unexpected final bytes_up/down: got (%d,%d), want (100,200)", up, down)
	}
}

// TestStore_ConcurrentAccess запускает множество сессий параллельно и
// проверяет, что записи в таблицах консистентны.
func TestStore_ConcurrentAccess(t *testing.T) {
	s, err := NewStore(":memory:", 5*time.Millisecond)
	if err != nil {
		t.Fatalf("NewStore(:memory:) failed: %v", err)
	}
	defer s.Close()

	var wg sync.WaitGroup
	const workers = 10
	const perWorker = 20

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("sess-%d-%d", worker, i)
				s.StartSession(SessionStart{
					ID:        id,
					Protocol:  "plain",
					Transport: "/proxy/1.0.0",
					StartedAt: time.Now(),
				})
				s.UpdateSession(id, int64(i), int64(i*2))
				s.AddQualitySample(id, time.Now(), int64(i), int64(i/2), 0.0)
				s.AddEvent(id, "concurrent", time.Now(), map[string]any{"worker": worker, "idx": i})
				s.EndSession(SessionEnd{
					ID:        id,
					BytesUp:   int64(i),
					BytesDown: int64(i * 2),
					Success:   true,
					EndedAt:   time.Now(),
				})
			}
		}(w)
	}

	wg.Wait()

	// Ждём, пока все операции будут сброшены в базу.
	expectedSessions := workers * perWorker
	waitUntil(t, 2*time.Second, func() bool {
		var count int
		row := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`)
		if err := row.Scan(&count); err != nil {
			return false
		}
		return count == expectedSessions
	})

	// Проверяем, что quality_samples и events тоже не пустые и примерно соответствуют ожиданиям.
	var qsCount, evCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM quality_samples`).Scan(&qsCount); err != nil {
		t.Fatalf("scan quality_samples total count failed: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&evCount); err != nil {
		t.Fatalf("scan events total count failed: %v", err)
	}

	if qsCount < expectedSessions {
		t.Fatalf("expected at least %d quality_samples, got %d", expectedSessions, qsCount)
	}
	if evCount < expectedSessions {
		t.Fatalf("expected at least %d events, got %d", expectedSessions, evCount)
	}
}

