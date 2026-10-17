package support

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// evalDir — набор И-16 в репозитории.
const evalDir = "../../eval/support"

// newStore — копия eval-набора во временном каталоге: тесты пишут, набор
// не меняется.
func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{UsersFile, TicketsFile} {
		b, err := os.ReadFile(filepath.Join(evalDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOpenReadsEvalSet(t *testing.T) {
	s := newStore(t)
	if n := len(s.Users()); n != 4 {
		t.Errorf("пользователей %d, ожидалось 4", n)
	}
	if n := len(s.Tickets()); n != 12 {
		t.Errorf("тикетов %d, ожидалось 12", n)
	}
	u, ok := s.User("u-agent-31")
	if !ok || u.Role != RoleAgent || u.Tools["node"] || !u.Tools["go"] {
		t.Errorf("u-agent-31: %+v, ok=%v", u, ok)
	}
	if _, ok := s.User("нет-такого"); ok {
		t.Error("несуществующий пользователь найден")
	}
	tk, ok := s.Ticket("T-08")
	if !ok || tk.User != "u-agent-31" || tk.Status != StatusOpen || tk.Num != "31" {
		t.Errorf("T-08: %+v, ok=%v", tk, ok)
	}
	if len(tk.Events) != 1 || tk.Events[0].Kind != EventCreated || tk.Events[0].At.IsZero() {
		t.Errorf("T-08: события %+v", tk.Events)
	}
	if tk.Expect == nil || tk.Expect.Action != ActionResolve || len(tk.Evidence) == 0 {
		t.Errorf("T-08: ожидания стенда не прочитались: %+v", tk.Expect)
	}
	pub := tk.Public()
	if pub.Expect != nil || pub.Evidence != nil || pub.Text != tk.Text {
		t.Errorf("Public: %+v", pub)
	}
	if _, ok := s.Ticket("T-99"); ok {
		t.Error("несуществующий тикет найден")
	}
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err == nil {
		t.Error("пустой каталог открылся")
	}
	write := func(users, tickets string) error {
		os.WriteFile(filepath.Join(dir, UsersFile), []byte(users), 0o644)
		os.WriteFile(filepath.Join(dir, TicketsFile), []byte(tickets), 0o644)
		_, err := Open(dir)
		return err
	}
	cases := []struct{ name, users, tickets, want string }{
		{"битый json", `[{"id": "u1"}`, `[]`, "users.json"},
		{"повтор пользователя", `[{"id": "u1"}, {"id": "u1"}]`, `[]`, "повторяется"},
		{"чужой пользователь", `[{"id": "u1"}]`, `[{"id": "T-1", "user": "u2", "subject": "x", "text": "y"}]`, "не найден"},
		{"неизвестный статус", `[{"id": "u1"}]`, `[{"id": "T-1", "user": "u1", "subject": "x", "text": "y", "status": "closed"}]`, "статус"},
		{"повтор тикета", `[{"id": "u1"}]`, `[{"id": "T-1", "user": "u1"}, {"id": "T-1", "user": "u1"}]`, "повторяется"},
	}
	for _, c := range cases {
		err := write(c.users, c.tickets)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, ожидалось «%s»", c.name, err, c.want)
		}
	}
	// BOM и тикет без статуса и событий — допустимо: статус open, событие created.
	err := write("\xEF\xBB\xBF"+`[{"id": "u1", "name": "Ё"}]`, `[{"id": "T-1", "user": "u1", "subject": "x", "text": "y"}]`)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := Open(dir)
	tk, _ := s.Ticket("T-1")
	if tk.Status != StatusOpen || len(tk.Events) != 1 || tk.Events[0].Kind != EventCreated {
		t.Errorf("тикет без статуса: %+v", tk)
	}
}

func TestSearch(t *testing.T) {
	s := newStore(t)
	if n := len(s.Search("", "", "")); n != 12 {
		t.Errorf("пустой запрос: %d, ожидалось 12", n)
	}
	// Регистр и ё/е не важны; тема раньше текста.
	got := s.Search("ТЕСТЫ НЕ ЗАПУСКАЮТСЯ", "", "")
	if len(got) != 2 || got[0].ID != "T-09" || got[1].ID != "T-10" {
		t.Errorf("близнецы: %v", ids(got))
	}
	got = s.Search("тесты не запускаются", "u-peer", "")
	if len(got) != 1 || got[0].ID != "T-10" {
		t.Errorf("по пользователю: %v", ids(got))
	}
	if got := s.Search("race", "", ""); len(got) != 1 || got[0].ID != "T-01" {
		t.Errorf("race: %v", ids(got))
	}
	// Совпадение по attach тоже считается, но ниже темы.
	if got := s.Search("Failed to load resource", "", ""); len(got) != 1 || got[0].ID != "T-03" {
		t.Errorf("attach: %v", ids(got))
	}
	if got := s.Search("", "", StatusResolved); len(got) != 0 {
		t.Errorf("resolved в исходном наборе: %v", ids(got))
	}
	if got := s.Search("", "u-peer", StatusOpen); len(got) != 3 {
		t.Errorf("u-peer open: %v", ids(got))
	}
	if got := s.Search("kubernetes", "", ""); len(got) != 1 || got[0].ID != "T-11" {
		t.Errorf("kubernetes: %v", ids(got))
	}
}

func ids(ts []Ticket) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func TestNoteStatusAndSave(t *testing.T) {
	s := newStore(t)
	fixed := time.Date(2026, 10, 16, 12, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	s.now = func() time.Time { return fixed }

	if err := s.AddNote("T-08", "по aitr doctor 31: DEEPSEEK_API_KEY — не задан"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote("T-08", "   "); err == nil {
		t.Error("пустая заметка принята")
	}
	if err := s.AddNote("T-99", "x"); err == nil {
		t.Error("заметка в несуществующий тикет принята")
	}
	if err := s.AddEvent("T-08", EventReply, "ответ"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEvent("T-08", EventStatus, "x"); err == nil {
		t.Error("status через AddEvent принят")
	}
	if s.StatusCalls() != 0 {
		t.Errorf("StatusCalls до SetStatus = %d", s.StatusCalls())
	}
	if err := s.SetStatus("T-08", "closed", ""); err == nil {
		t.Error("неизвестный статус принят")
	}
	if err := s.SetStatus("T-08", StatusResolved, "ключ не задан — впиши в .env"); err != nil {
		t.Fatal(err)
	}
	if s.StatusCalls() != 1 {
		t.Errorf("StatusCalls = %d, ожидалось 1 (неизвестный статус отвергается до счётчика)", s.StatusCalls())
	}
	tk, _ := s.Ticket("T-08")
	if tk.Status != StatusResolved {
		t.Errorf("статус %q", tk.Status)
	}
	kinds := []string{}
	for _, e := range tk.Events {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "created,note,reply,status" {
		t.Errorf("события: %v", kinds)
	}
	last := tk.Events[len(tk.Events)-1]
	if !last.At.Equal(fixed) || !strings.HasPrefix(last.Text, "resolved: ") {
		t.Errorf("событие status: %+v", last)
	}
	if !tk.Updated().Equal(fixed) {
		t.Errorf("Updated = %v", tk.Updated())
	}

	// Копия отвязана от хранилища.
	tk.Events[0].Text = "подмена"
	again, _ := s.Ticket("T-08")
	if again.Events[0].Text == "подмена" {
		t.Error("Ticket вернул не копию")
	}

	// Сохранилось: перечитываем файл, ожидания стенда на месте, остальные
	// тикеты не тронуты; формат — JSON с отступами, без BOM, с \n в конце.
	b, err := os.ReadFile(filepath.Join(s.Dir(), TicketsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "[\n  {") || !strings.HasSuffix(string(b), "\n") {
		t.Errorf("формат файла: %q…", string(b[:20]))
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), TicketsFile+".tmp")); err == nil {
		t.Error("временный файл остался")
	}
	s2, err := Open(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	t2, _ := s2.Ticket("T-08")
	if t2.Status != StatusResolved || len(t2.Events) != 4 || t2.Expect == nil || len(t2.Evidence) != 4 {
		t.Errorf("после перечитывания: status=%s events=%d expect=%v evidence=%d", t2.Status, len(t2.Events), t2.Expect != nil, len(t2.Evidence))
	}
	if t1, _ := s2.Ticket("T-01"); t1.Status != StatusOpen || len(t1.Events) != 1 {
		t.Errorf("T-01 изменился: %+v", t1)
	}
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw[0]["evidence"]; !has {
		t.Error("evidence потерялся при сохранении")
	}
	// Файл users.json код не пишет.
	ub, _ := os.ReadFile(filepath.Join(s.Dir(), UsersFile))
	eb, _ := os.ReadFile(filepath.Join(evalDir, UsersFile))
	if string(ub) != string(eb) {
		t.Error("users.json изменён")
	}
}

func TestEscalateAndReopen(t *testing.T) {
	s := newStore(t)
	if err := s.SetStatus("T-11", StatusEscalated, "какой кластер и кто его администрирует?"); err != nil {
		t.Fatal(err)
	}
	tk, _ := s.Ticket("T-11")
	if tk.Status != StatusEscalated || !strings.Contains(tk.Events[len(tk.Events)-1].Text, "escalated: какой кластер") {
		t.Errorf("escalate: %+v", tk)
	}
	if got := s.Search("", "", StatusEscalated); len(got) != 1 || got[0].ID != "T-11" {
		t.Errorf("поиск escalated: %v", ids(got))
	}
	if err := s.SetStatus("T-11", StatusOpen, ""); err != nil {
		t.Fatal(err)
	}
	tk, _ = s.Ticket("T-11")
	if tk.Status != StatusOpen || tk.Events[len(tk.Events)-1].Text != "open" {
		t.Errorf("reopen: %+v", tk.Events)
	}
}

func TestConcurrentWrites(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "T-0" + string(rune('1'+i%5))
			if i%4 == 0 {
				_ = s.SetStatus(id, StatusResolved, "готово")
			} else {
				_ = s.AddNote(id, "заметка")
			}
			s.Search("тест", "", "")
			s.Tickets()
		}(i)
	}
	wg.Wait()
	s2, err := Open(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, tk := range s2.Tickets() {
		total += len(tk.Events) - 1
	}
	if total != 20 {
		t.Errorf("событий после 20 записей: %d", total)
	}
	if s.StatusCalls() != 5 {
		t.Errorf("StatusCalls = %d", s.StatusCalls())
	}
}
