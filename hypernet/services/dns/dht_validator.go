package dns

import (
	"encoding/json"
	"fmt"

	"github.com/libp2p/go-libp2p-record"
)

// dhtRecordValidator реализует record.Validator для ключей DHT с namespace /hypernet/name/.
type dhtRecordValidator struct{}

// Validate проверяет, что value — валидная JSON-запись Record с непустой подписью.
func (dhtRecordValidator) Validate(key string, value []byte) error {
	if len(value) == 0 {
		return fmt.Errorf("dns: empty record")
	}
	var rec Record
	if err := json.Unmarshal(value, &rec); err != nil {
		return fmt.Errorf("dns: invalid record json: %w", err)
	}
	if rec.Signature == "" {
		return fmt.Errorf("dns: record missing signature")
	}
	return nil
}

// Select возвращает индекс записи с максимальным Timestamp (наиболее свежая).
func (dhtRecordValidator) Select(key string, values [][]byte) (int, error) {
	if len(values) == 0 {
		return 0, fmt.Errorf("dns: no values to select")
	}
	best := -1
	var bestTs int64
	for i, v := range values {
		var rec Record
		if err := json.Unmarshal(v, &rec); err != nil {
			continue
		}
		if best < 0 || rec.Timestamp > bestTs {
			best = i
			bestTs = rec.Timestamp
		}
	}
	if best < 0 {
		return 0, fmt.Errorf("dns: no valid record in set")
	}
	return best, nil
}

// DHTRecordValidator возвращает валидатор для регистрации в DHT (NamespacePrefix без завершающего слэша).
func DHTRecordValidator() record.Validator {
	return dhtRecordValidator{}
}

