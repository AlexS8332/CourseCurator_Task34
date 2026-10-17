package support

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/corpus"
)

// courseRoot — корень курса: CURATOR_COURSE_ROOT или каталог с CLAUDE.md
// и scripts/aitr.ps1 вверх по дереву от пакета; пусто — корня нет
// (чужая машина, снимок задания).
func courseRoot(t *testing.T) string {
	t.Helper()
	if r := strings.TrimSpace(os.Getenv("CURATOR_COURSE_ROOT")); r != "" {
		return r
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "scripts", "aitr.ps1")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// keyLike — то, что финишёр считает значением ключа; в наборе таких
// строк быть не должно, иначе стенд ловил бы собственные данные.
var keyLike = regexp.MustCompile(`sk-[A-Za-z0-9]+|[A-Za-z0-9]{32,}`)

// TestTicketSetShape — состав набора И-16: 12 тикетов, 4 пользователя
// (автор на двух машинах, однокурсник на macOS, агент в задании 31);
// 8 корпусных с must, sources, evidence и action reply|resolve; 2
// близнеца с одинаковым текстом, разным стеком и must_not чужого стека;
// 2 вне корпуса — escalate без evidence. Все открыты, у всех есть
// событие created с временем.
func TestTicketSetShape(t *testing.T) {
	s, err := Open(evalDir)
	if err != nil {
		t.Fatal(err)
	}
	users := s.Users()
	if len(users) != 4 {
		t.Fatalf("пользователей %d", len(users))
	}
	roles := map[string]int{}
	for _, u := range users {
		roles[u.Role]++
		if u.OS == "" || u.Shell == "" || len(u.Tools) == 0 {
			t.Errorf("%s: нет os/shell/tools", u.ID)
		}
	}
	if roles[RoleAuthor] != 2 || roles[RolePeer] != 1 || roles[RoleAgent] != 1 {
		t.Errorf("роли: %v", roles)
	}
	tks := s.Tickets()
	if len(tks) != 12 {
		t.Fatalf("тикетов %d", len(tks))
	}
	var corpusN, escalateN int
	twins := map[string][]Ticket{}
	for _, tk := range tks {
		if tk.Status != StatusOpen || len(tk.Events) == 0 || tk.Events[0].Kind != EventCreated || tk.Events[0].At.IsZero() {
			t.Errorf("%s: не открыт или нет created с временем", tk.ID)
		}
		if tk.Subject == "" || tk.Text == "" {
			t.Errorf("%s: пустая тема или текст", tk.ID)
		}
		if tk.Expect == nil {
			t.Errorf("%s: нет expect", tk.ID)
			continue
		}
		for _, field := range []string{tk.Text, tk.Attach, tk.Subject} {
			if m := keyLike.FindString(field); m != "" {
				t.Errorf("%s: похоже на значение ключа: %q", tk.ID, m)
			}
		}
		switch tk.Expect.Action {
		case ActionReply, ActionResolve:
			corpusN++
			if len(tk.Expect.Must) == 0 || len(tk.Expect.Sources) == 0 || len(tk.Evidence) == 0 {
				t.Errorf("%s: корпусному тикету нужны must, sources и evidence", tk.ID)
			}
			srcs := map[string]bool{}
			for _, src := range tk.Expect.Sources {
				srcs[src] = true
			}
			for _, e := range tk.Evidence {
				if !srcs[e.DocID] && !strings.HasPrefix(e.DocID, "docs/cards/") {
					t.Errorf("%s: evidence из %s, которого нет в sources", tk.ID, e.DocID)
				}
			}
			twins[tk.Text] = append(twins[tk.Text], tk)
		case ActionEscalate:
			escalateN++
			if len(tk.Evidence) > 0 || len(tk.Expect.Sources) > 0 || len(tk.Expect.Must) > 0 {
				t.Errorf("%s: у escalate не должно быть evidence, sources и must", tk.ID)
			}
		default:
			t.Errorf("%s: неизвестное действие %q", tk.ID, tk.Expect.Action)
		}
	}
	if corpusN != 10 || escalateN != 2 {
		t.Errorf("корпусных %d (8 + 2 близнеца), escalate %d", corpusN, escalateN)
	}
	var pair []Ticket
	for _, group := range twins {
		if len(group) == 2 {
			pair = group
		}
	}
	if len(pair) != 2 {
		t.Fatal("нет пары близнецов с одинаковым текстом")
	}
	a, b := pair[0], pair[1]
	if a.Stack == b.Stack || a.User == b.User || len(a.Expect.MustNot) == 0 || len(b.Expect.MustNot) == 0 {
		t.Errorf("близнецы: стек %s/%s, пользователи %s/%s, must_not %v/%v", a.Stack, b.Stack, a.User, b.User, a.Expect.MustNot, b.Expect.MustNot)
	}
	// Слова must одного близнеца запрещены у другого: иначе «один ответ на
	// оба» прошёл бы.
	forbids := func(x, y Ticket) bool {
		for _, g := range x.Expect.Must {
			for _, w := range g {
				for _, n := range y.Expect.MustNot {
					if strings.Contains(strings.ToLower(w), strings.ToLower(n)) || strings.Contains(strings.ToLower(n), strings.ToLower(w)) {
						return true
					}
				}
			}
		}
		return false
	}
	if !forbids(a, b) || !forbids(b, a) {
		t.Errorf("must_not близнецов не пересекается с must другого: %v / %v", a.Expect, b.Expect)
	}
	// Тикет про авторизацию — от агента в задании 31: ответ зависит от
	// aitr doctor по этому заданию.
	auth, ok := s.Ticket("T-08")
	if !ok || auth.Num != "31" || auth.User != "u-agent-31" || !strings.Contains(strings.ToLower(auth.Text), "авторизац") {
		t.Errorf("T-08: %+v", auth)
	}
}

// TestTicketEvidenceExists — каждая evidence.quote — дословная подстрока
// файла курса по doc_id (после нормализации ё/е, регистра и пробелов, как
// у cite); каждый expect.sources — существующий файл курса. Без корня
// курса — пропуск.
func TestTicketEvidenceExists(t *testing.T) {
	root := courseRoot(t)
	if root == "" {
		t.Skip("корня курса нет (CURATOR_COURSE_ROOT не задан, CLAUDE.md не найден)")
	}
	s, err := Open(evalDir)
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]string{}
	load := func(id, docID string) (string, bool) {
		if strings.Contains(docID, "\\") || strings.HasPrefix(docID, "/") {
			t.Errorf("%s: doc_id — относительный путь с «/»: %q", id, docID)
			return "", false
		}
		text, ok := cache[docID]
		if !ok {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(docID)))
			if err != nil {
				t.Errorf("%s: %v", id, err)
				return "", false
			}
			text = corpus.Normalize(string(b))
			cache[docID] = text
		}
		return text, true
	}
	for _, tk := range s.Tickets() {
		if tk.Expect != nil {
			for _, src := range tk.Expect.Sources {
				load(tk.ID, src)
			}
		}
		for i, e := range tk.Evidence {
			text, ok := load(tk.ID, e.DocID)
			if !ok {
				continue
			}
			if len([]rune(e.Quote)) < 15 {
				t.Errorf("%s evidence %d: цитата короче 15 символов", tk.ID, i)
			}
			if !strings.Contains(text, corpus.Normalize(e.Quote)) {
				t.Errorf("%s evidence %d: цитата не найдена в %s: %q", tk.ID, i, e.DocID, e.Quote)
			}
		}
	}
}
