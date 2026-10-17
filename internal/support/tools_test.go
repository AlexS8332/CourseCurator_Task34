package support

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/tools"
)

func call(t *testing.T, s *Store, name, args string) map[string]any {
	t.Helper()
	out, err := callErr(s, name, args)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%s: не JSON: %v\n%s", name, err, out)
	}
	return m
}

func callErr(s *Store, name, args string) (string, error) {
	for _, tl := range Tools(s) {
		if tl.Spec().Name == name {
			return tl.Call(context.Background(), json.RawMessage(args))
		}
	}
	return "", errors.New("нет инструмента " + name)
}

// TestToolSpecs — состав и признаки: четыре инструмента в порядке
// таблицы, crm_status нет, чтение — Untrusted, crm_note — Write;
// описания по-русски, схемы — корректный JSON, реестр собирается.
func TestToolSpecs(t *testing.T) {
	s := newStore(t)
	ts := Tools(s)
	if got := strings.Join(tools.Names(ts), ","); got != "crm_user,crm_ticket,crm_search,crm_note" {
		t.Errorf("инструменты: %s", got)
	}
	if strings.Join(Names(), ",") != strings.Join(tools.Names(ts), ",") {
		t.Error("Names расходится с Tools")
	}
	for _, tl := range ts {
		sp := tl.Spec()
		if sp.Name == "crm_status" {
			t.Error("crm_status — инструмент: запрет должен держаться отсутствием (ИП-7)")
		}
		if !json.Valid(sp.Parameters) {
			t.Errorf("%s: схема не JSON", sp.Name)
		}
		if !strings.ContainsAny(sp.Description, "абвгдежзийклмнопрстуфхцчшщъыьэюя") || len(sp.Description) < 200 {
			t.Errorf("%s: описание должно быть по-русски и как промпт: %q", sp.Name, sp.Description)
		}
		if sp.Via != tools.ViaLocal {
			t.Errorf("%s: via=%q", sp.Name, sp.Via)
		}
		wantUntrusted := sp.Name != CRMNote
		if sp.Untrusted != wantUntrusted || sp.Write != !wantUntrusted {
			t.Errorf("%s: untrusted=%v write=%v", sp.Name, sp.Untrusted, sp.Write)
		}
	}
	if _, err := tools.NewRegistry(ts...); err != nil {
		t.Error(err)
	}
	if fp := tools.Fingerprint(ts); fp != tools.Fingerprint(Tools(s)) {
		t.Error("отпечаток набора нестабилен")
	}
}

func TestCRMUser(t *testing.T) {
	s := newStore(t)
	m := call(t, s, CRMUser, `{"id": "u-peer"}`)
	u := m["user"].(map[string]any)
	if u["role"] != RolePeer || u["os"] != "macOS" || u["tools"].(map[string]any)["gh"] != false {
		t.Errorf("u-peer: %v", u)
	}
	if _, err := callErr(s, CRMUser, `{"id": "u-nobody"}`); err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Errorf("несуществующий: %v", err)
	}
	if _, err := callErr(s, CRMUser, `{"id": 5}`); err == nil {
		t.Error("число вместо id принято")
	}
}

func TestCRMTicketHidesExpect(t *testing.T) {
	s := newStore(t)
	out, err := callErr(s, CRMTicket, `{"id": " T-08 "}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "expect") || strings.Contains(out, "evidence") || strings.Contains(out, "must") {
		t.Errorf("ожидания стенда ушли модели: %s", out)
	}
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	tk := m["ticket"].(map[string]any)
	if tk["id"] != "T-08" || tk["user"] != "u-agent-31" || tk["status"] != StatusOpen || tk["num"] != "31" {
		t.Errorf("T-08: %v", tk)
	}
	if !strings.Contains(tk["attach"].(string), "401") {
		t.Errorf("attach: %v", tk["attach"])
	}
	ev := tk["events"].([]any)
	if len(ev) != 1 || ev[0].(map[string]any)["kind"] != EventCreated || !strings.HasPrefix(ev[0].(map[string]any)["at"].(string), "2026-10-15") {
		t.Errorf("events: %v", ev)
	}
	if _, err := callErr(s, CRMTicket, `{"id": "T-99"}`); err == nil {
		t.Error("несуществующий тикет найден")
	}
}

func TestCRMSearch(t *testing.T) {
	s := newStore(t)
	m := call(t, s, CRMSearch, ``)
	if m["count"].(float64) != 12 || m["total"].(float64) != 12 {
		t.Errorf("без аргументов: %v", m)
	}
	list := m["tickets"].([]any)
	first := list[0].(map[string]any)
	if first["id"] != "T-01" || first["events"].(float64) != 1 || first["subject"] == "" {
		t.Errorf("строка выдачи: %v", first)
	}
	if _, has := first["text"]; has {
		t.Error("в выдаче поиска текст тикета целиком — лишнее")
	}
	m = call(t, s, CRMSearch, `{"q": "тесты не запускаются", "user": "u-peer"}`)
	if m["count"].(float64) != 1 || m["tickets"].([]any)[0].(map[string]any)["stack"] != "python" {
		t.Errorf("близнец u-peer: %v", m)
	}
	m = call(t, s, CRMSearch, `{"status": "resolved"}`)
	if m["count"].(float64) != 0 {
		t.Errorf("resolved: %v", m)
	}
	if _, err := callErr(s, CRMSearch, `{"status": "closed"}`); err == nil {
		t.Error("неизвестный статус принят")
	}
}

func TestCRMNote(t *testing.T) {
	s := newStore(t)
	m := call(t, s, CRMNote, `{"id": "T-08", "text": "aitr doctor 31: DEEPSEEK_API_KEY — не задан"}`)
	if m["ok"] != true || m["events"].(float64) != 2 || m["status"] != StatusOpen {
		t.Errorf("crm_note: %v", m)
	}
	tk, _ := s.Ticket("T-08")
	if tk.Events[1].Kind != EventNote || tk.Events[1].At.IsZero() {
		t.Errorf("событие: %+v", tk.Events[1])
	}
	if _, err := callErr(s, CRMNote, `{"id": "T-08", "text": ""}`); err == nil {
		t.Error("пустая заметка принята")
	}
	if _, err := callErr(s, CRMNote, `{"id": "T-99", "text": "x"}`); err == nil {
		t.Error("заметка в несуществующий тикет принята")
	}
	// Заметка статус не меняет и SetStatus не зовёт.
	if s.StatusCalls() != 0 {
		t.Errorf("StatusCalls = %d", s.StatusCalls())
	}
	if _, has := m["expect"]; has {
		t.Error("expect в ответе crm_note")
	}
}
