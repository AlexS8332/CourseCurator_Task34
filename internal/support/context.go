package support

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/tools"
)

// Контекст тикета — то, что код знает о пользователе и его среде до того,
// как модель прочитала обращение (механизм features.SupportContext).
//
// Блок собирает код, а не пересказ человека: пользователь и его машина —
// из CRM, задание — из assignment.json, ключи и автор коммитов — из живого
// `aitr doctor`, последний прогон — из report.json. Модель получает
// ≈200 токенов фактов, которых в тексте тикета нет («ключ не задан» видно
// только по doctor), и не тратит шаги на их добывание. Выключенный
// механизм — контрольная дорожка стенда: блока нет, инструменты CRM
// остаются.

// DoctorTTL — сколько держится вывод aitr doctor по заданию: ход поддержки
// и повторный вызов aitr_doctor моделью идут минутами, а doctor — секунды
// PowerShell. Тот же срок, что у инструмента aitr_doctor.
const DoctorTTL = project.DoctorTTL

// doctorCache — вывод aitr doctor по корню и номеру задания.
var doctorCache = struct {
	mu   sync.Mutex
	done map[string]doctorEntry
}{done: map[string]doctorEntry{}}

type doctorEntry struct {
	out string
	at  time.Time
}

// ErrNoTicket — тикета с таким id в хранилище нет.
var ErrNoTicket = errors.New("тикет не найден")

// Gather собирает контекст тикета: тикет и пользователь — из Store,
// задание — d.Root.Assignment(ticket.Num), вывод doctor — d.PS («aitr
// doctor NN», кэш DoctorTTL), последний прогон — d.Root.LastRun. Нет
// тикета — ошибка; всё остальное необязательно: без корня курса или без
// PowerShell блок короче, но ход идёт.
func Gather(ctx context.Context, d project.Deps, s *Store, id string) (Context, error) {
	if s == nil {
		return Context{}, errors.New("хранилище CRM не открыто")
	}
	id = strings.TrimSpace(id)
	t, ok := s.Ticket(id)
	if !ok {
		return Context{}, fmt.Errorf("%w: %s", ErrNoTicket, id)
	}
	c := Context{Ticket: t}
	if u, ok := s.User(t.User); ok {
		c.User = u
	} else {
		c.User = User{ID: t.User}
	}
	num := strings.TrimSpace(t.Num)
	if d.Root == nil || num == "" {
		return c, nil
	}
	a, err := d.Root.Assignment(num)
	if err != nil {
		return c, nil
	}
	c.Assignment = &a
	if rep, ok, err := d.Root.LastRun(a.Num); err == nil && ok {
		c.LastRun = &rep
	}
	if d.PS != nil {
		c.Doctor = doctor(ctx, d, a.Num)
	}
	return c, nil
}

// doctor — вывод aitr doctor по заданию с кэшем. Ошибка команды — не
// повод терять вывод: doctor печатает FAIL и возвращает ненулевой код, а
// именно строки FAIL и нужны блоку.
func doctor(ctx context.Context, d project.Deps, num string) string {
	key := d.Root.Dir + "\x00" + num
	doctorCache.mu.Lock()
	e, hit := doctorCache.done[key]
	doctorCache.mu.Unlock()
	if hit && time.Since(e.at) <= DoctorTTL {
		return e.out
	}
	out, err := d.PS.Run(ctx, "doctor", num)
	if strings.TrimSpace(out) == "" {
		if err != nil {
			return ""
		}
		return ""
	}
	if ctx.Err() == nil {
		doctorCache.mu.Lock()
		doctorCache.done[key] = doctorEntry{out: out, at: time.Now()}
		doctorCache.mu.Unlock()
	}
	return out
}

// ResetDoctorCache — забыть вывод doctor (тесты, смена корня).
func ResetDoctorCache() {
	doctorCache.mu.Lock()
	doctorCache.done = map[string]doctorEntry{}
	doctorCache.mu.Unlock()
}

// BlockTitle — заголовок блока «Контекст тикета».
const BlockTitle = "Контекст тикета"

// maxDoctorLines — сколько строк doctor попадает в блок: ключи, автор
// коммитов и FAIL — остальное (версии go, карточка, реестр) модель при
// нужде возьмёт вызовом aitr_doctor.
const maxDoctorLines = 8

// Block — блок «Контекст тикета» для запроса (≈200 токенов): пользователь
// и его машина, задание, ключи «задан / не задан» и автор коммитов по
// doctor, последний прогон (упавший шаг, js_errors). Чего код не знает,
// того в блоке нет — модель видит «doctor не запускался», а не догадку.
func (c Context) Block() string {
	var b strings.Builder
	b.WriteString(BlockTitle + " (собран кодом из CRM и среды; " + tools.TrustNote + ")\n")
	t := c.Ticket
	fmt.Fprintf(&b, "- Тикет %s: статус %s", orDash(t.ID), orDash(t.Status))
	if t.Num != "" {
		fmt.Fprintf(&b, ", задание %s", t.Num)
	}
	if t.Stack != "" {
		fmt.Fprintf(&b, ", стек %s", t.Stack)
	}
	if n := len(t.Events); n > 1 {
		fmt.Fprintf(&b, ", событий в истории %d", n)
	}
	b.WriteString(".\n")
	b.WriteString("- Пользователь: " + userLine(c.User) + "\n")
	if a := c.Assignment; a != nil {
		fmt.Fprintf(&b, "- Задание %s (%s): стек %s, статус %s", a.Num, a.DirName(), orDash(a.Stack), orDash(a.Status))
		if len(a.Env) > 0 {
			fmt.Fprintf(&b, ", нужны ключи %s", strings.Join(a.Env, ", "))
		} else {
			b.WriteString(", ключи не нужны")
		}
		b.WriteString(".\n")
	} else if t.Num != "" {
		fmt.Fprintf(&b, "- Задание %s: assignment.json не найден.\n", t.Num)
	}
	switch lines := DoctorLines(c.Doctor); {
	case len(lines) > 0:
		b.WriteString("- aitr doctor: " + strings.Join(lines, "; ") + ".\n")
	case c.Assignment != nil:
		b.WriteString("- aitr doctor не запускался (вызови aitr_doctor при нужде).\n")
	}
	if r := c.LastRun; r != nil {
		b.WriteString("- Последний прогон: " + runLine(*r) + "\n")
	} else if c.Assignment != nil {
		b.WriteString("- Контрольных прогонов с записью не было.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// userLine — пользователь одной строкой: имя, роль, машина, инструменты,
// задания.
func userLine(u User) string {
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name = orDash(u.ID)
	}
	s := name + " (" + roleName(u.Role) + ")"
	var env []string
	if u.OS != "" {
		env = append(env, u.OS)
	}
	if u.Shell != "" {
		env = append(env, u.Shell)
	}
	if len(env) > 0 {
		s += ", " + strings.Join(env, ", ")
	}
	if len(u.Tools) > 0 {
		var have, miss []string
		for _, n := range sortedTools(u.Tools) {
			if u.Tools[n] {
				have = append(have, n)
			} else {
				miss = append(miss, n)
			}
		}
		if len(have) > 0 {
			s += "; есть " + strings.Join(have, ", ")
		}
		if len(miss) > 0 {
			s += "; нет " + strings.Join(miss, ", ")
		}
	}
	if len(u.Assignments) > 0 {
		s += "; задания " + strings.Join(u.Assignments, ", ")
	}
	return s + "."
}

// roleName — роль словами.
func roleName(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleAuthor:
		return "автор курса"
	case RolePeer:
		return "однокурсник"
	case RoleAgent:
		return "агент Claude Code"
	case "":
		return "роль не указана"
	}
	return role
}

func sortedTools(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DoctorLines — строки вывода aitr doctor, которые нужны блоку: ключи
// («<ИМЯ> - задан в …» / «<ИМЯ> - не задан»), автор коммитов и все FAIL.
// Значений ключей doctor не печатает — только имена. Без заголовков и
// пометок ok/warn/FAIL в начале; не больше maxDoctorLines.
func DoctorLines(out string) []string {
	var lines []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(out, "\n") {
		l := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if l == "" {
			continue
		}
		mark, rest, ok := strings.Cut(l, " ")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		switch mark {
		case "ok", "warn", "FAIL":
		default:
			continue
		}
		keep := mark == "FAIL" || strings.Contains(rest, "автор коммитов") ||
			strings.Contains(rest, " - задан") || strings.Contains(rest, " - не задан") ||
			strings.Contains(rest, "не требует ключей") || strings.Contains(rest, "корневой .env")
		if !keep || seen[rest] {
			continue
		}
		seen[rest] = true
		line := rest
		if mark == "FAIL" {
			line = "FAIL " + rest
		}
		lines = append(lines, line)
		if len(lines) >= maxDoctorLines {
			break
		}
	}
	return lines
}

// runLine — последний прогон одной строкой: прошёл ли, проверки, упавший
// шаг и ошибки JS.
func runLine(r course.RunReport) string {
	s := "пройден"
	if !r.Passed {
		s = "не пройден"
	}
	s += fmt.Sprintf(", проверок %d/%d", r.ChecksOK, r.Checks)
	if f := r.Failed; f != nil {
		s += fmt.Sprintf("; упал шаг %d (%s", f.N, f.Action)
		if f.Target != "" {
			s += " " + f.Target
		}
		if f.Error != "" {
			s += ": " + clipRunes(f.Error, 120)
		}
		s += ")"
	}
	if len(r.JSErrors) > 0 {
		s += fmt.Sprintf("; js_errors %d: %s", len(r.JSErrors), clipRunes(strings.Join(r.JSErrors, " | "), 160))
	}
	return s + "."
}

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
