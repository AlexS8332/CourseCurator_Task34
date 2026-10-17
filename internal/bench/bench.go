// Package bench — стенд испытаний куратора (И-14…И-18): дорожки,
// отличающиеся ровно одним механизмом (ИП-13), проверки порогов кодом и
// отчёт. Из v30 скопирована только CheckLanes; Lane/Result/Check/Markdown —
// свои, без истории диалогов и профилей.
package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/llm"
)

// ErrLanes — дорожки стенда отличаются не ровно одним механизмом.
var ErrLanes = errors.New("дорожки стенда должны отличаться ровно одним механизмом")

// Lane — дорожка стенда: имя и набор механизмов. Same — дорожка с тем же
// набором, что у основной: повтор для замера шума модели. Разницы
// механизмов она не добавляет, но и скрыть её не может: у стенда всё равно
// должна быть дорожка, отличающаяся ровно одним механизмом.
type Lane struct {
	Name string `json:"name"`
	// Note — чем дорожка отличается, словами для отчёта.
	Note     string       `json:"note,omitempty"`
	Features features.Set `json:"features"`
	Same     bool         `json:"same,omitempty"`
}

// CheckLanes проверяет правило ИП-13 и возвращает механизм, который стенд
// сравнивает. Первая дорожка — основная, с ней сверяются остальные.
// (Копия v30 internal/bench/bench.go:61–100.)
func CheckLanes(reg *features.Registry, lanes []Lane) (features.Name, error) {
	if len(lanes) < 2 {
		return "", fmt.Errorf("%w: у стенда %d дорожек, сравнивать не с чем", ErrLanes, len(lanes))
	}
	base := reg.Complete(lanes[0].Features)
	names := map[string]bool{}
	var compared []features.Name
	seen := map[features.Name]bool{}
	for i, l := range lanes {
		if strings.TrimSpace(l.Name) == "" {
			return "", fmt.Errorf("у дорожки %d нет имени", i+1)
		}
		if names[l.Name] {
			return "", fmt.Errorf("дорожка «%s» объявлена дважды", l.Name)
		}
		names[l.Name] = true
		fs := reg.Complete(l.Features)
		if err := reg.Validate(fs); err != nil {
			return "", fmt.Errorf("дорожка «%s»: %w", l.Name, err)
		}
		if i == 0 {
			continue
		}
		diff := reg.Diff(base, fs)
		if l.Same {
			if len(diff) != 0 {
				return "", fmt.Errorf("%w: «%s» помечена как та же, что «%s», но отличается механизмами %v", ErrLanes, l.Name, lanes[0].Name, diff)
			}
			continue
		}
		switch len(diff) {
		case 0:
			return "", fmt.Errorf("%w: «%s» не отличается от «%s» ни одним механизмом; повтор дорожки помечается Same", ErrLanes, l.Name, lanes[0].Name)
		case 1:
		default:
			return "", fmt.Errorf("%w: «%s» отличается от «%s» механизмами %s", ErrLanes, l.Name, lanes[0].Name, diffNames(diff))
		}
		if !seen[diff[0]] {
			seen[diff[0]] = true
			compared = append(compared, diff[0])
		}
	}
	switch len(compared) {
	case 0:
		return "", fmt.Errorf("%w: ни одна дорожка не отличается от «%s»", ErrLanes, lanes[0].Name)
	case 1:
		return compared[0], nil
	}
	return "", fmt.Errorf("%w: дорожки сравнивают разные механизмы %v", ErrLanes, diffNames(compared))
}

func diffNames(ns []features.Name) string {
	if len(ns) == 0 {
		return "(никакими)"
	}
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = string(n)
	}
	return strings.Join(parts, ", ")
}

// Check — одна проверка порога: что проверяли, на какой дорожке, прошла
// ли, что получилось и что требовалось.
type Check struct {
	What string `json:"what"`
	Lane string `json:"lane"`
	OK   bool   `json:"ok"`
	Got  string `json:"got"`
	Need string `json:"need"`
}

// Metric — отчётная величина без порога (по дорожке).
type Metric struct {
	Name  string  `json:"name"`
	Lane  string  `json:"lane"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// Result — итог одного стенда испытания.
type Result struct {
	Trial string `json:"trial"`
	// Stand — имя стенда внутри испытания (у И-14 их два: docs и state).
	Stand string `json:"stand,omitempty"`
	// Mechanism — механизм, который сравнивают дорожки.
	Mechanism features.Name `json:"mechanism"`
	Model     string        `json:"model,omitempty"`
	Started   time.Time     `json:"started"`
	Seconds   float64       `json:"seconds"`
	Lanes     []Lane        `json:"lanes"`
	Checks    []Check       `json:"checks"`
	Metrics   []Metric      `json:"metrics"`
	Cost      llm.Cost      `json:"cost"`
	Requests  int           `json:"requests"`
	// Rows — ответы по вопросам и дорожкам для разбора руками.
	Rows []Row `json:"rows,omitempty"`
	// Notes — что стенд хочет сказать словами (откаты, предупреждения).
	Notes []string `json:"notes,omitempty"`
}

// Row — один ответ на стенде.
type Row struct {
	Lane     string   `json:"lane"`
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Split    string   `json:"split"`
	Question string   `json:"q"`
	Status   string   `json:"status"`
	Answer   string   `json:"answer"`
	OK       bool     `json:"ok"`
	Problems []string `json:"problems,omitempty"`
	Steps    int      `json:"steps"`
	Calls    []string `json:"calls,omitempty"`
	Seconds  float64  `json:"seconds"`
}

// Passed — все проверки стенда прошли.
func (r *Result) Passed() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// check — добавить проверку.
func (r *Result) check(what, lane string, ok bool, got, need string) {
	r.Checks = append(r.Checks, Check{What: what, Lane: lane, OK: ok, Got: got, Need: need})
}

func (r *Result) metric(name, lane string, v float64, unit, note string) {
	r.Metrics = append(r.Metrics, Metric{Name: name, Lane: lane, Value: v, Unit: unit, Note: note})
}

// Markdown — отчёт по стендам: дорожки, проверки, метрики, цена.
func Markdown(results []*Result) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n")
		}
		title := r.Trial
		if r.Stand != "" {
			title += " · стенд «" + r.Stand + "»"
		}
		verdict := "пройдено"
		if !r.Passed() {
			verdict = "НЕ пройдено"
		}
		fmt.Fprintf(&b, "# %s — %s\n\n", title, verdict)
		fmt.Fprintf(&b, "| | |\n|---|---|\n| Механизм | `%s` |\n| Модель | %s |\n| Начало | %s |\n| Длительность | %.0f с |\n| Запросов | %d |\n| Цена | %s |\n\n",
			r.Mechanism, orDash(r.Model), r.Started.Format("2006-01-02 15:04"), r.Seconds, r.Requests, usd(r.Cost))
		b.WriteString("## Дорожки\n\n| Дорожка | Механизмы | Примечание |\n|---|---|---|\n")
		for _, l := range r.Lanes {
			note := l.Note
			if l.Same {
				note = strings.TrimSpace("повтор основной (шум) " + note)
			}
			fmt.Fprintf(&b, "| %s | `%s` | %s |\n", l.Name, orDash(l.Features.String()), orDash(note))
		}
		b.WriteString("\n## Проверки\n\n| | Что | Дорожка | Получено | Порог |\n|---|---|---|---|---|\n")
		for _, c := range r.Checks {
			mark := "✓"
			if !c.OK {
				mark = "✗"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", mark, c.What, c.Lane, c.Got, c.Need)
		}
		if len(r.Metrics) > 0 {
			b.WriteString("\n## Метрики\n\n| Метрика | Дорожка | Значение | Примечание |\n|---|---|---|---|\n")
			for _, m := range r.Metrics {
				fmt.Fprintf(&b, "| %s | %s | %s%s | %s |\n", m.Name, m.Lane, trimFloat(m.Value), unit(m.Unit), orDash(m.Note))
			}
		}
		if len(r.Notes) > 0 {
			b.WriteString("\n## Замечания\n\n")
			for _, n := range r.Notes {
				b.WriteString("- " + n + "\n")
			}
		}
		if len(r.Rows) > 0 {
			b.WriteString("\n## Ответы\n\n| Дорожка | Вопрос | Статус | Принят | Шагов | Ответ |\n|---|---|---|---|---|---|\n")
			for _, row := range r.Rows {
				ok := "да"
				if !row.OK {
					ok = "нет: " + strings.Join(row.Problems, "; ")
				}
				fmt.Fprintf(&b, "| %s | %s %s | %s | %s | %d | %s |\n", row.Lane, row.ID, cell(row.Question, 60), row.Status, cell(ok, 80), row.Steps, cell(row.Answer, 120))
			}
		}
	}
	return b.String()
}

// Write кладёт отчёт markdown и JSON в каталог: <dir>/<name>.md и .json.
func Write(dir, name string, results []*Result) (md, js string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	md = filepath.Join(dir, name+".md")
	js = filepath.Join(dir, name+".json")
	if err := os.WriteFile(md, []byte(Markdown(results)), 0o644); err != nil {
		return "", "", err
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(js, data, 0o644); err != nil {
		return "", "", err
	}
	return md, js, nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func unit(u string) string {
	if u == "" {
		return ""
	}
	return " " + u
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// usd — цена строкой; неизвестный прайс — «?».
func usd(c llm.Cost) string {
	if !c.Known {
		return "?"
	}
	return fmt.Sprintf("$%.4f", c.USD)
}

// cell — текст в ячейку таблицы: одна строка, без «|», не длиннее n рун.
func cell(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "|", "¦")), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ratio — «k/n».
func ratio(k, n int) string { return fmt.Sprintf("%d/%d", k, n) }

// sortedKeys — ключи карты по алфавиту.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
