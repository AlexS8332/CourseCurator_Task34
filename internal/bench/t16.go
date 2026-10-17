package bench

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/support"
)

// И-16 — «поддержка пользователей оснастки курса» (день 33).
//
// Набор eval/support: users.json и tickets.json — 8 тикетов по корпусу
// (ответ есть в карточках, README, SKILL, FAQ), 2 близнеца (один текст от
// пользователей со стеком go и python — ответы должны разойтись по стеку)
// и 2 вне корпуса (escalate). Один стенд, три дорожки, отличающиеся ровно
// одним механизмом (ИП-13): основная / без блока «Контекст тикета»
// (support.context выкл, инструменты CRM остаются) / повтор для шума.
//
// Стенд работает на КОПИИ набора: каждая дорожка получает свой каталог
// во временном (или заданном) рабочем каталоге, и resolve/escalate хода
// меняют статусы в копии, а не в eval/support. Пороги — в проверках ниже;
// «SetStatus до принятого resolve — 0» считается по событиям тикетов в
// копии и счётчику Store.StatusCalls.

// Trial16 — имя испытания.
const Trial16 = "И-16"

// Категории тикетов в отчёте (Row.Type).
const (
	TicketCorpus = "corpus"
	TicketTwin   = "twin"
	TicketOut    = "out"
)

// Регэкспы утечек в тексте ответа — те же, что у финишёра support_reply.
var (
	t16ChunkRe  = regexp.MustCompile(`/structure/\d+`)
	t16SecretRe = regexp.MustCompile(`sk-[A-Za-z0-9]+|[A-Za-z0-9]{32,}`)
)

// T16Options — параметры прогона.
type T16Options struct {
	Deps  project.Deps
	LLM   llm.Chatter
	Model string
	// Set — каталог набора (eval/support с users.json и tickets.json).
	Set string
	// Work — куда класть копии набора по дорожкам; пусто — временный
	// каталог, который удаляется после прогона.
	Work string
	// Log — ход прогона строками; nil — молча.
	Log io.Writer
}

// T16 гоняет стенд И-16 и возвращает его результат (один стенд — один
// элемент, как у T14 по стендам).
func T16(ctx context.Context, o T16Options) ([]*Result, error) {
	if o.LLM == nil {
		return nil, fmt.Errorf("нет модели")
	}
	if strings.TrimSpace(o.Set) == "" {
		return nil, fmt.Errorf("не указан каталог набора (eval/support)")
	}
	for _, f := range []string{support.UsersFile, support.TicketsFile} {
		if _, err := os.Stat(filepath.Join(o.Set, f)); err != nil {
			return nil, fmt.Errorf("набор %s: %w", o.Set, err)
		}
	}
	work := o.Work
	if work == "" {
		tmp, err := os.MkdirTemp("", "t16-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		work = tmp
	}
	reg := features.Catalog()
	base := reg.Defaults()
	lanes := []Lane{
		{Name: "main", Features: base, Note: "основная: блок «Контекст тикета», CRM, docs_search"},
		{Name: "noctx", Features: base.With(support.Mechanism, false), Note: "без блока контекста (инструменты CRM остаются)"},
		{Name: "same", Features: base, Same: true},
	}
	mech, err := CheckLanes(reg, lanes)
	if err != nil {
		return nil, err
	}
	// Категории тикетов — по эталонному набору, до копий.
	src, err := support.Open(o.Set)
	if err != nil {
		return nil, err
	}
	tickets := src.Tickets()
	if len(tickets) == 0 {
		return nil, fmt.Errorf("в наборе %s нет тикетов", o.Set)
	}
	kinds := Categorize(tickets)
	r := &Result{Trial: Trial16, Stand: "tickets", Mechanism: mech, Model: o.Model, Started: time.Now(), Lanes: lanes}
	if o.Log != nil {
		fmt.Fprintf(o.Log, "%s · стенд tickets: %d тикетов × %d дорожек; набор %s, копии в %s\n", Trial16, len(tickets), len(lanes), o.Set, work)
	}
	runs := map[string]*lane16{}
	for _, l := range lanes {
		lr, err := run16(ctx, o, l, tickets, kinds, filepath.Join(work, l.Name), r)
		if err != nil {
			return []*Result{r}, err
		}
		runs[l.Name] = lr
		r.Cost = r.Cost.Add(lr.cost)
		r.Requests += lr.reqs
		r.Rows = append(r.Rows, lr.rows...)
	}
	r.Seconds = time.Since(r.Started).Seconds()

	tl := map[string]tally16{}
	for name, lr := range runs {
		tl[name] = lr.tally
	}
	m, same, noctx := tl["main"], tl["same"], tl["noctx"]
	mr := runs["main"]
	r.check("корпус: reply|resolve с источником и дословной цитатой", "main", m.cited == m.corpusN, ratio(m.cited, m.corpusN), ratio(m.corpusN, m.corpusN))
	needMust := max(m.corpusN-1, 0)
	r.check("корпус: must", "main", m.must >= needMust, ratio(m.must, m.corpusN), "≥ "+ratio(needMust, m.corpusN))
	r.check("близнецы: ответ под свой стек (must и must_not)", "main", m.twinsOK == m.twinsN, ratio(m.twinsOK, m.twinsN), ratio(m.twinsN, m.twinsN))
	r.check("вне корпуса: escalate", "main", m.outEsc == m.outN, ratio(m.outEsc, m.outN), ratio(m.outN, m.outN))
	r.check("SetStatus до принятого resolve/escalate", "main", m.statusExtra == 0, strconv.Itoa(m.statusExtra), "0")
	r.check("chunk_id и значения ключей в тексте", "main", m.leaks == 0, strconv.Itoa(m.leaks), "0")
	r.check("«не проверено»", "main", m.unverified == 0, strconv.Itoa(m.unverified), "0")
	avg := float64(mr.steps) / float64(max(m.n, 1))
	r.check("запросов на ход (среднее)", "main", avg <= MaxStepsAvg, trimFloat(avg), fmt.Sprintf("≤ %s", trimFloat(MaxStepsAvg)))
	// Контроль «без контекста»: инструменты CRM остаются, и агент добирает
	// сведения о пользователе вызовами (crm_user, crm_search, aitr_doctor) —
	// поэтому близнецы могут разойтись и без блока, а при двух парах разница
	// неотличима от шума (считается отчётно). Что даёт механизм измеримо —
	// дешёвый ход: без блока запросов на ход больше, чем шум между основной
	// дорожкой и повтором.
	avgNoctx := float64(runs["noctx"].steps) / float64(max(noctx.n, 1))
	avgSame := float64(runs["same"].steps) / float64(max(same.n, 1))
	stepNoise := math.Abs(avg - avgSame)
	r.check("без контекста: запросов на ход больше шума", "noctx", avgNoctx-avg > stepNoise,
		fmt.Sprintf("noctx %s, main %s, повтор %s (шум %s)", trimFloat(avgNoctx), trimFloat(avg), trimFloat(avgSame), trimFloat(stepNoise)), "рост > шум")
	r.metric("близнецы: падение без контекста", "noctx", float64(m.twinsOK-noctx.twinsOK), "", fmt.Sprintf("шум %d — отчётно", abs(m.twinsOK-same.twinsOK)))
	for _, name := range sortedKeys(tl) {
		t, lr := tl[name], runs[name]
		r.metric("корпус: с источником и цитатой", name, float64(t.cited), "", ratio(t.cited, t.corpusN))
		r.metric("корпус: must", name, float64(t.must), "", ratio(t.must, t.corpusN))
		r.metric("корпус: ожидаемый источник (doc_id)", name, float64(t.srcOK), "", ratio(t.srcOK, t.corpusN)+" — отчётно")
		r.metric("близнецы по своему стеку", name, float64(t.twinsOK), "", ratio(t.twinsOK, t.twinsN))
		r.metric("вне корпуса: escalate", name, float64(t.outEsc), "", ratio(t.outEsc, t.outN))
		r.metric("resolve принято", name, float64(t.resolved), "", ratio(t.resolved, t.n))
		r.metric("SetStatus сверх принятых", name, float64(t.statusExtra), "", "")
		r.metric("утечки chunk_id/ключей", name, float64(t.leaks), "", "")
		r.metric("«не проверено»", name, float64(t.unverified), "", "")
		r.metric("запросов на ход", name, float64(lr.steps)/float64(max(t.n, 1)), "", "")
		r.metric("постоянная часть", name, float64(lr.constant), "токенов", "наибольшая за дорожку")
		r.metric("цена", name, lr.cost.USD, "$", lr.cost.Tariff)
		r.metric("время", name, lr.secs, "с", "")
	}
	return []*Result{r}, nil
}

// tally16 — счётчики одной дорожки.
type tally16 struct {
	n, corpusN, cited, must, srcOK, twinsN, twinsOK, outN, outEsc int
	resolved, statusExtra, leaks, unverified                      int
}

// lane16 — ответы одной дорожки.
type lane16 struct {
	lane     Lane
	tally    tally16
	rows     []Row
	cost     llm.Cost
	reqs     int
	steps    int
	constant int
	secs     float64
}

// run16 — все тикеты по дорожке на своей копии набора.
func run16(ctx context.Context, o T16Options, lane Lane, tickets []support.Ticket, kinds map[string]string, dir string, r *Result) (*lane16, error) {
	if err := CopySet(o.Set, dir); err != nil {
		return nil, fmt.Errorf("копия набора для дорожки «%s»: %w", lane.Name, err)
	}
	s, err := support.Open(dir)
	if err != nil {
		return nil, err
	}
	lr := &lane16{lane: lane}
	for _, tk := range tickets {
		started := time.Now()
		t, err := support.Run(ctx, o.LLM, o.Deps, s, tk.ID, nil, support.Options{Model: o.Model, Features: lane.Features})
		if t == nil {
			return nil, fmt.Errorf("дорожка «%s», тикет %s: %w", lane.Name, tk.ID, err)
		}
		secs := time.Since(started).Seconds()
		lr.cost = lr.cost.Add(t.Stats.Cost)
		lr.reqs += t.Stats.Steps
		lr.steps += t.Stats.Steps
		lr.secs += secs
		if c := t.Stats.Context.Estimate.Constant; c > lr.constant {
			lr.constant = c
		}
		kind := kinds[tk.ID]
		row := Row{Lane: lane.Name, ID: tk.ID, Type: kind, Question: tk.Subject, Status: t.Reply.Action,
			Answer: t.Reply.Answer, OK: !t.Unverified() && err == nil, Steps: t.Stats.Steps, Seconds: secs}
		if err != nil {
			row.Problems = append(row.Problems, err.Error())
			r.Notes = append(r.Notes, fmt.Sprintf("дорожка «%s», %s: ход оборвался: %v", lane.Name, tk.ID, err))
		}
		row.Problems = append(row.Problems, t.Check.Problems...)
		if t.Calls != nil {
			for _, c := range t.Calls.Calls() {
				if !c.Final {
					row.Calls = append(row.Calls, c.Tool)
				}
			}
		}
		lr.rows = append(lr.rows, row)
		count16(&lr.tally, kind, tk, t, s)
		if o.Log != nil {
			fmt.Fprintf(o.Log, "  %-6s %-5s %-8s %d шаг. %s\n", lane.Name, tk.ID, t.Reply.Action, t.Stats.Steps, cell(t.Reply.Answer, 70))
		}
	}
	return lr, nil
}

// count16 — счётчики по одному ходу.
func count16(t *tally16, kind string, tk support.Ticket, turn *support.Turn, s *support.Store) {
	t.n++
	if turn.Unverified() {
		t.unverified++
	}
	a := turn.Reply
	if turn.Applied == support.StatusResolved {
		t.resolved++
	}
	if t16ChunkRe.MatchString(a.Answer+"\n"+a.Note) || t16SecretRe.MatchString(a.Answer+"\n"+a.Note) {
		t.leaks++
	}
	// Статус менялся только кодом после принятого resolve/escalate: событий
	// status у тикета не больше, чем принятых действий.
	expected := 0
	if turn.Applied == support.StatusResolved || turn.Applied == support.StatusEscalated {
		expected = 1
	}
	if after, ok := s.Ticket(tk.ID); ok {
		got := 0
		for _, e := range after.Events {
			if e.Kind == support.EventStatus {
				got++
			}
		}
		if got > expected {
			t.statusExtra += got - expected
		}
	}
	answered := a.Action == support.ActionReply || a.Action == support.ActionResolve
	switch kind {
	case TicketOut:
		t.outN++
		if a.Action == support.ActionEscalate {
			t.outEsc++
		}
	case TicketTwin:
		t.twinsN++
		if answered && !turn.Unverified() && ExpectOK(a.Answer, tk.Expect) {
			t.twinsOK++
		}
	default:
		t.corpusN++
		ck := turn.Check
		if answered && ck.OK && !ck.Unverified && ck.HasSources && ck.HasQuotes && ck.Verbatim >= 1 {
			t.cited++
		}
		if answered && ExpectOK(a.Answer, tk.Expect) {
			t.must++
		}
		if answered && SourcesOK(a.Sources, tk.Expect) {
			t.srcOK++
		}
	}
}

// Categorize — категория каждого тикета: out — ожидается escalate; twin —
// тикеты с одинаковым текстом (после нормализации) от разных
// пользователей; остальное — corpus.
func Categorize(tickets []support.Ticket) map[string]string {
	byText := map[string][]string{}
	for _, t := range tickets {
		if t.Expect != nil && t.Expect.Action == support.ActionEscalate {
			continue
		}
		key := corpus.Normalize(t.Text)
		byText[key] = append(byText[key], t.ID)
	}
	out := map[string]string{}
	for _, t := range tickets {
		switch {
		case t.Expect != nil && t.Expect.Action == support.ActionEscalate:
			out[t.ID] = TicketOut
		case len(byText[corpus.Normalize(t.Text)]) > 1:
			out[t.ID] = TicketTwin
		default:
			out[t.ID] = TicketCorpus
		}
	}
	return out
}

// ExpectOK — в ответе есть хотя бы одна форма из каждой группы must и нет
// слов must_not (после нормализации); нет ожиданий — всегда да.
func ExpectOK(text string, e *support.Expect) bool {
	if e == nil {
		return true
	}
	return MustOK(text, &kb.Expect{Must: e.Must, MustNot: e.MustNot})
}

// SourcesOK — хотя бы один источник ответа лежит в ожидаемом документе
// (chunk_id начинается с doc_id); ожиданий нет — да.
func SourcesOK(src []cite.CitedSource, e *support.Expect) bool {
	if e == nil || len(e.Sources) == 0 {
		return true
	}
	for _, s := range src {
		id := strings.TrimSpace(s.ChunkID)
		for _, doc := range e.Sources {
			doc = strings.TrimSuffix(strings.TrimSpace(doc), "/")
			if doc != "" && (id == doc || strings.HasPrefix(id, doc+"/")) {
				return true
			}
		}
	}
	return false
}

// CopySet — копия набора (users.json, tickets.json) в каталог dst: стенд
// и команда support работают на копии, чтобы не портить eval/support.
func CopySet(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, f := range []string{support.UsersFile, support.TicketsFile} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
