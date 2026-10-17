package support

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aitr/34-files-agent/internal/corpus"
)

// Имена файлов CRM в каталоге хранилища.
const (
	UsersFile   = "users.json"
	TicketsFile = "tickets.json"
)

// Statuses — допустимые статусы тикета по порядку показа.
var Statuses = []string{StatusOpen, StatusResolved, StatusEscalated}

// Store — CRM в двух JSON-файлах каталога: users.json (массив User) и
// tickets.json (массив Ticket). Пользователей код не меняет; тикеты —
// только через AddNote/AddEvent/SetStatus, и после каждой записи файл
// сохраняется целиком (запись во временный файл и переименование).
// Потокобезопасен: окно и стенд ходят в него из разных горутин.
//
// StatusCalls считает вызовы SetStatus: стенд И-16 проверяет, что до
// принятого resolve статус не менялся — у модели инструмента crm_status
// нет (ИП-7), а значит счётчик растёт только из кода.
type Store struct {
	dir string
	now func() time.Time

	mu          sync.RWMutex
	users       []User
	tickets     []Ticket
	statusCalls int
}

// Open читает каталог CRM: eval-набор (eval/support) или рабочую копию
// (data/support). Оба файла обязательны; битый JSON — ошибка с именем
// файла. Тикет без статуса считается открытым, без событий — получает
// событие created с нулевым временем (история из файла не выдумывается).
func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, now: time.Now}
	if err := readJSON(filepath.Join(dir, UsersFile), &s.users); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, TicketsFile), &s.tickets); err != nil {
		return nil, err
	}
	seenU := map[string]bool{}
	for _, u := range s.users {
		if u.ID == "" {
			return nil, fmt.Errorf("%s: пользователь без id", UsersFile)
		}
		if seenU[u.ID] {
			return nil, fmt.Errorf("%s: id %q повторяется", UsersFile, u.ID)
		}
		seenU[u.ID] = true
	}
	seenT := map[string]bool{}
	for i := range s.tickets {
		t := &s.tickets[i]
		if t.ID == "" {
			return nil, fmt.Errorf("%s: тикет %d без id", TicketsFile, i)
		}
		if seenT[t.ID] {
			return nil, fmt.Errorf("%s: id %q повторяется", TicketsFile, t.ID)
		}
		seenT[t.ID] = true
		if t.Status == "" {
			t.Status = StatusOpen
		}
		if !validStatus(t.Status) {
			return nil, fmt.Errorf("%s: тикет %s: неизвестный статус %q", TicketsFile, t.ID, t.Status)
		}
		if !seenU[t.User] {
			return nil, fmt.Errorf("%s: тикет %s: пользователь %q не найден в %s", TicketsFile, t.ID, t.User, UsersFile)
		}
		if len(t.Events) == 0 {
			t.Events = []Event{{Kind: EventCreated, Text: "тикет создан"}}
		}
	}
	return s, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	b = []byte(strings.TrimPrefix(string(b), "\xEF\xBB\xBF"))
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

func validStatus(st string) bool {
	for _, s := range Statuses {
		if s == st {
			return true
		}
	}
	return false
}

// Dir — каталог хранилища.
func (s *Store) Dir() string { return s.dir }

// Users — все пользователи в порядке файла (копия).
func (s *Store) Users() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u.clone())
	}
	return out
}

// User — пользователь по id.
func (s *Store) User(id string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if u.ID == id {
			return u.clone(), true
		}
	}
	return User{}, false
}

// Ticket — тикет по id (копия: события и ожидания отвязаны от хранилища).
func (s *Store) Ticket(id string) (Ticket, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i := s.index(id); i >= 0 {
		return s.tickets[i].clone(), true
	}
	return Ticket{}, false
}

// Tickets — все тикеты в порядке файла (копии).
func (s *Store) Tickets() []Ticket {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Ticket, 0, len(s.tickets))
	for _, t := range s.tickets {
		out = append(out, t.clone())
	}
	return out
}

// Search — тикеты по подстроке q (id, тема, текст, приложение; регистр,
// ё/е и пробелы — как у corpus.Normalize), пользователю и статусу. Пустой
// аргумент не ограничивает. Порядок: сначала совпадения по id и теме,
// потом по тексту; внутри — порядок файла.
func (s *Store) Search(q, user, status string) []Ticket {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q = corpus.Normalize(strings.TrimSpace(q))
	type hit struct {
		rank int
		t    Ticket
	}
	var hits []hit
	for _, t := range s.tickets {
		if user != "" && t.User != user {
			continue
		}
		if status != "" && t.Status != status {
			continue
		}
		rank := 0
		if q != "" {
			switch {
			case strings.Contains(corpus.Normalize(t.ID), q), strings.Contains(corpus.Normalize(t.Subject), q):
				rank = 1
			case strings.Contains(corpus.Normalize(t.Text), q), strings.Contains(corpus.Normalize(t.Attach), q):
				rank = 2
			default:
				continue
			}
		}
		hits = append(hits, hit{rank, t.clone()})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	out := make([]Ticket, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.t)
	}
	return out
}

// AddEvent — событие в историю тикета (kind: reply | note | escalate …) и
// сохранение. Статус не меняет — для него SetStatus.
func (s *Store) AddEvent(id, kind, text string) error {
	kind = strings.TrimSpace(kind)
	if kind == "" || kind == EventStatus {
		return fmt.Errorf("тикет %s: событие %q не добавляется через AddEvent", id, kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return fmt.Errorf("тикет %s не найден", id)
	}
	s.tickets[i].Events = append(s.tickets[i].Events, Event{At: s.now(), Kind: kind, Text: text})
	return s.save()
}

// AddNote — заметка в тикет (событие note) и сохранение. Пустая заметка —
// ошибка: модель иногда зовёт crm_note «на всякий случай».
func (s *Store) AddNote(id, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("тикет %s: пустая заметка", id)
	}
	return s.AddEvent(id, EventNote, text)
}

// SetStatus — смена статуса (open | resolved | escalated) с событием
// status (text — ответ или вопрос, с которым тикет закрыт/передан) и
// сохранением. Зовёт только код — после принятого финишёра или кнопки в
// окне; инструмента для модели нет.
func (s *Store) SetStatus(id, status, text string) error {
	if !validStatus(status) {
		return fmt.Errorf("тикет %s: неизвестный статус %q (open | resolved | escalated)", id, status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCalls++
	i := s.index(id)
	if i < 0 {
		return fmt.Errorf("тикет %s не найден", id)
	}
	t := &s.tickets[i]
	t.Status = status
	label := status
	if strings.TrimSpace(text) != "" {
		label += ": " + text
	}
	t.Events = append(t.Events, Event{At: s.now(), Kind: EventStatus, Text: label})
	return s.save()
}

// StatusCalls — сколько раз звали SetStatus с допустимым статусом
// (и для несуществующего тикета тоже: стенд считает попытки сменить
// статус, а не успехи; неизвестный статус — ошибка программиста, он
// отвергается до счётчика).
func (s *Store) StatusCalls() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statusCalls
}

// Save — записать tickets.json (под замком; снаружи — после ручной
// правки через Ticket/копию не нужен, все записи сохраняются сами).
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save()
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.tickets, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	path := filepath.Join(s.dir, TicketsFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows: переименование поверх открытого файла может не пройти —
		// пишем напрямую.
		if werr := os.WriteFile(path, b, 0o644); werr != nil {
			os.Remove(tmp)
			return errors.Join(err, werr)
		}
		os.Remove(tmp)
	}
	return nil
}

// index — позиция тикета по id; -1 — нет. Под замком вызывающего.
func (s *Store) index(id string) int {
	for i := range s.tickets {
		if s.tickets[i].ID == id {
			return i
		}
	}
	return -1
}

// Public — тикет без полей стенда (Expect, Evidence): таким его видят
// модель и окно.
func (t Ticket) Public() Ticket {
	c := t.clone()
	c.Expect = nil
	c.Evidence = nil
	return c
}

// Updated — время последнего события (нулевое — событий с временем нет).
func (t Ticket) Updated() time.Time {
	var at time.Time
	for _, e := range t.Events {
		if e.At.After(at) {
			at = e.At
		}
	}
	return at
}

func (t Ticket) clone() Ticket {
	c := t
	c.Events = append([]Event(nil), t.Events...)
	c.Evidence = append([]Evidence(nil), t.Evidence...)
	if t.Expect != nil {
		e := *t.Expect
		e.Must = make([][]string, len(t.Expect.Must))
		for i, g := range t.Expect.Must {
			e.Must[i] = append([]string(nil), g...)
		}
		e.MustNot = append([]string(nil), t.Expect.MustNot...)
		e.Sources = append([]string(nil), t.Expect.Sources...)
		c.Expect = &e
	}
	return c
}

func (u User) clone() User {
	c := u
	if u.Tools != nil {
		c.Tools = make(map[string]bool, len(u.Tools))
		for k, v := range u.Tools {
			c.Tools[k] = v
		}
	}
	c.Assignments = append([]string(nil), u.Assignments...)
	return c
}
