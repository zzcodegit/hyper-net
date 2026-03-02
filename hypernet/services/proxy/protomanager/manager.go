package protomanager

import (
	"sort"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

// Manager управляет набором доступных протоколов и выбором общего протокола
// между двумя сторонами.
type Manager struct {
	protocols map[string]protoiface.Protocol
	// preferred задаёт глобальный порядок предпочтения протоколов.
	preferred          []string
	registrationOrder  []string
	defaultTransport   string
	supportedTransports []string // транспорты, поддерживаемые сервером (для согласования)
}

// NewManager создаёт новый менеджер протоколов.
// preferred может быть nil/пустым – тогда порядок важности задаётся порядком регистрации.
func NewManager(preferred []string) *Manager {
	// Копируем слайс, чтобы не зависеть от внешних модификаций.
	var prefsCopy []string
	if len(preferred) > 0 {
		prefsCopy = append(prefsCopy, preferred...)
	}
	return &Manager{
		protocols:         make(map[string]protoiface.Protocol),
		preferred:         prefsCopy,
		registrationOrder: nil,
	}
}

// Register добавляет/обновляет протокол в менеджере.
func (m *Manager) Register(p protoiface.Protocol) {
	if p == nil {
		return
	}
	name := p.Name()
	if name == "" {
		return
	}
	if m.protocols == nil {
		m.protocols = make(map[string]protoiface.Protocol)
	}
	if _, exists := m.protocols[name]; !exists {
		m.registrationOrder = append(m.registrationOrder, name)
	}
	m.protocols[name] = p
}

// Get возвращает адаптер протокола по имени, если он зарегистрирован.
func (m *Manager) Get(name string) (protoiface.Protocol, bool) {
	p, ok := m.protocols[name]
	return p, ok
}

// SetDefaultTransport задаёт транспорт по умолчанию для рукопожатий (например, "tcp", "ws").
func (m *Manager) SetDefaultTransport(name string) {
	m.defaultTransport = name
}

// DefaultTransport возвращает заданный транспорт по умолчанию; пустая строка — не задан.
func (m *Manager) DefaultTransport() string {
	return m.defaultTransport
}

// SetSupportedTransports задаёт список транспортов для согласования на сервере (например, ["tcp", "ws"]).
func (m *Manager) SetSupportedTransports(transports []string) {
	m.supportedTransports = append([]string(nil), transports...)
}

// SupportedTransports возвращает список поддерживаемых транспортов; по умолчанию ["tcp"].
func (m *Manager) SupportedTransports() []string {
	if len(m.supportedTransports) > 0 {
		return m.supportedTransports
	}
	return []string{"tcp"}
}

// ChooseCommonTransport выбирает первый транспорт из clientPreferred, который поддерживается сервером.
func (m *Manager) ChooseCommonTransport(clientPreferred []string) string {
	serverSet := make(map[string]struct{})
	for _, t := range m.SupportedTransports() {
		serverSet[t] = struct{}{}
	}
	for _, t := range clientPreferred {
		if t == "" {
			continue
		}
		if _, ok := serverSet[t]; ok {
			return t
		}
	}
	return ""
}

// SupportedNames возвращает отсортированный список имён протоколов,
// известных менеджеру. Порядок соответствует preferred (если задан)
// или порядку регистрации.
func (m *Manager) SupportedNames() []string {
	if len(m.protocols) == 0 {
		return nil
	}
	names := make([]string, 0, len(m.protocols))
	for name := range m.protocols {
		names = append(names, name)
	}

	order := m.effectivelyPreferredOrder()
	if len(order) == 0 {
		sort.Strings(names)
		return names
	}

	// Сортируем согласно order: чем меньше индекс, тем выше приоритет.
	priority := make(map[string]int, len(order))
	for i, name := range order {
		priority[name] = i
	}
	sort.Slice(names, func(i, j int) bool {
		pi, okI := priority[names[i]]
		pj, okJ := priority[names[j]]
		switch {
		case okI && okJ:
			return pi < pj
		case okI && !okJ:
			return true
		case !okI && okJ:
			return false
		default:
			return names[i] < names[j]
		}
	})
	return names
}

// ChooseCommon выбирает общий протокол между локальным и удалённым списком имён.
// Возвращает имя протокола и true, если найдено совпадение.
// Приоритет задаётся порядком имён в localSupported: первым идёт наиболее
// предпочтительный вариант с точки зрения вызывающей стороны.
func (m *Manager) ChooseCommon(localSupported, remoteSupported []string) (string, bool) {
	if len(localSupported) == 0 || len(remoteSupported) == 0 {
		return "", false
	}

	remoteSet := make(map[string]struct{}, len(remoteSupported))
	for _, name := range remoteSupported {
		remoteSet[name] = struct{}{}
	}

	// Приоритет задаётся localSupported: выбираем первый протокол, который
	// поддерживается и локальной, и удалённой стороной.
	for _, name := range localSupported {
		if _, ok := remoteSet[name]; ok {
			return name, true
		}
	}

	return "", false
}

// effectivelyPreferredOrder возвращает итоговый приоритет имен протоколов:
// либо preferred, либо порядок регистрации, если preferred пуст.
func (m *Manager) effectivelyPreferredOrder() []string {
	if len(m.preferred) > 0 {
		return m.preferred
	}
	return m.registrationOrder
}

func contains(slice []string, target string) bool {
	for _, v := range slice {
		if v == target {
			return true
		}
	}
	return false
}

