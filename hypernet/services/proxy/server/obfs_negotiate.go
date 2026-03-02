package proxy

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const obfsPrefix = "OBFS "

// ObfsParams — параметры обфускации для согласования.
type ObfsParams struct {
	PaddingMin, PaddingMax       int
	WriteDelayMin, WriteDelayMax time.Duration
}

// FormatObfsLine формирует строку запроса/ответа OBFS для передачи по потоку.
// Формат: "OBFS <pmin> <pmax> <dmin> <dmax>\n", длительности в Go-формате (0, 10ms, 1s).
func FormatObfsLine(p ObfsParams) string {
	dmin := "0"
	if p.WriteDelayMin > 0 {
		dmin = p.WriteDelayMin.String()
	}
	dmax := "0"
	if p.WriteDelayMax > 0 {
		dmax = p.WriteDelayMax.String()
	}
	return fmt.Sprintf("%s%d %d %s %s\n", obfsPrefix, p.PaddingMin, p.PaddingMax, dmin, dmax)
}

// ParseObfsLine разбирает строку "OBFS <pmin> <pmax> <dmin> <dmax>" и возвращает параметры.
// Возвращает ok=false при ошибке разбора.
func ParseObfsLine(line string) (p ObfsParams, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, obfsPrefix) {
		return ObfsParams{}, false
	}
	rest := strings.TrimSpace(line[len(obfsPrefix):])
	parts := strings.Fields(rest)
	if len(parts) != 4 {
		return ObfsParams{}, false
	}
	if p.PaddingMin, ok = parseInt(parts[0]); !ok {
		return ObfsParams{}, false
	}
	if p.PaddingMax, ok = parseInt(parts[1]); !ok {
		return ObfsParams{}, false
	}
	if p.PaddingMin < 0 || p.PaddingMax < 0 || p.PaddingMin > p.PaddingMax {
		return ObfsParams{}, false
	}
	dmin, err := time.ParseDuration(parts[2])
	if err != nil || dmin < 0 {
		return ObfsParams{}, false
	}
	dmax, err := time.ParseDuration(parts[3])
	if err != nil || dmax < 0 || dmax < dmin {
		return ObfsParams{}, false
	}
	p.WriteDelayMin = dmin
	p.WriteDelayMax = dmax
	return p, true
}

func parseInt(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// AgreeObfs возвращает параметры, согласованные между клиентом и лимитами сервера:
// если обфускация на сервере выключена (paddingMax/writeDelayMax == 0), возвращаются нули;
// иначе паддинг и задержки ограничиваются лимитами сервера.
func (s *Server) AgreeObfs(client ObfsParams) ObfsParams {
	agreed := client
	if s.paddingMax <= 0 {
		agreed.PaddingMin, agreed.PaddingMax = 0, 0
	} else {
		if agreed.PaddingMax > s.paddingMax {
			agreed.PaddingMax = s.paddingMax
		}
		if agreed.PaddingMin > agreed.PaddingMax {
			agreed.PaddingMin = agreed.PaddingMax
		}
		if agreed.PaddingMin < s.paddingMin {
			agreed.PaddingMin = s.paddingMin
		}
	}
	if s.writeDelayMax <= 0 {
		agreed.WriteDelayMin, agreed.WriteDelayMax = 0, 0
	} else {
		if agreed.WriteDelayMax > s.writeDelayMax {
			agreed.WriteDelayMax = s.writeDelayMax
		}
		if agreed.WriteDelayMin > agreed.WriteDelayMax {
			agreed.WriteDelayMin = agreed.WriteDelayMax
		}
		if agreed.WriteDelayMin < s.writeDelayMin {
			agreed.WriteDelayMin = s.writeDelayMin
		}
	}
	return agreed
}

