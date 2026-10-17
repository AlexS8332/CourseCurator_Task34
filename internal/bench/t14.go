package bench

import (
	"context"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/tools"
)

// И-14 — «/help понимает каталог курса» (день 31).
//
// Набор eval/help.json: 10 docs/test, 6 docs/dev, 4 state, 4 out. Два
// стенда, у каждого дорожки отличаются ровно одним механизмом (ИП-13):
//   - docs: основная / без базы (help.rag выкл) / повтор — вопросы docs и out;
//   - state: основная / без инструментов (help.tools выкл) / повтор — вопросы state.
//
// Эталон вопросов о состоянии считает код в момент прогона (ветка — git,
// статусы и число заданий — assignment.json, последний прогон —
// report.json), иначе набор протухал бы с каждым коммитом. Пороги — в
// проверках ниже; судья-модель (-judge) — только отчётно и в день 31 не
// подключён.

// Trial14 — имя испытания.
const Trial14 = "И-14"

// Пороги И-14.
const (
	// MaxStepsAvg — запросов к модели на ход в среднем.
	MaxStepsAvg = 4.0
	// MaxConstant — постоянная часть запроса (промпт + инструменты), токенов.
	MaxConstant = 6000
	// MaxFalseUnknown — «не знаю» на отвечаемых вопросах test.
	MaxFalseUnknown = 1
)

// Типы вопросов И-14 (kb.Question.Type).
const (
	TypeDocs  = "docs"
	TypeState = "state"
	TypeOut   = "out"
)

// Reference — эталон вопросов о состоянии, посчитанный кодом.
type Reference struct {
	Branch  string   `json:"branch"`
	NotDone []string `json:"not_done"`
	Count   int      `json:"count"`
	// LastRun — последний прогон задания из вопроса ref:last-run:NN.
	LastRun map[string]RunRef `json:"last_run,omitempty"`
}

// RunRef — что код знает о последнем прогоне задания.
type RunRef struct {
	Has      bool `json:"has"`
	Passed   bool `json:"passed"`
	Checks   int  `json:"checks"`
	ChecksOK int  `json:"checks_ok"`
}

// ReferenceOf считает эталон по каталогу курса: ветка корня, задания не
// done, число заданий, последние прогоны заданий из вопросов.
func ReferenceOf(ctx context.Context, root *course.Root, qs []kb.Question) (Reference, error) {
	if root == nil {
		return Reference{}, fmt.Errorf("каталог курса не открыт")
	}
	ref := Reference{LastRun: map[string]RunRef{}}
	branch, err := gitx.Repo{Dir: root.Dir}.Branch(ctx)
	if err != nil {
		return ref, fmt.Errorf("ветка корня: %w", err)
	}
	ref.Branch = strings.TrimSpace(branch)
	as, err := root.Assignments()
	if err != nil {
		return ref, fmt.Errorf("задания: %w", err)
	}
	ref.Count = len(as)
	for _, a := range as {
		if a.Status != "done" {
			ref.NotDone = append(ref.NotDone, a.Num)
		}
	}
	for _, q := range qs {
		kind, num := refKind(q)
		if kind != "last-run" || num == "" {
			continue
		}
		r, ok, err := root.LastRun(num)
		if err != nil {
			return ref, fmt.Errorf("последний прогон %s: %w", num, err)
		}
		ref.LastRun[num] = RunRef{Has: ok, Passed: r.Passed, Checks: r.Checks, ChecksOK: r.ChecksOK}
	}
	return ref, nil
}

// refKind — вид эталона вопроса о состоянии из note «ref:branch»,
// «ref:not-done», «ref:count», «ref:last-run:30».
func refKind(q kb.Question) (kind, num string) {
	note := strings.TrimSpace(q.Note)
	if !strings.HasPrefix(note, "ref:") {
		return "", ""
	}
	parts := strings.SplitN(strings.TrimPrefix(note, "ref:"), ":", 2)
	kind = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		num = strings.TrimSpace(parts[1])
	}
	return kind, num
}

// T14Options — параметры прогона.
type T14Options struct {
	Deps  project.Deps
	LLM   llm.Chatter
	Model string
	// Questions — путь к eval/help.json.
	Questions string
	// Stands — какие стенды гонять: docs, state; пусто — оба.
	Stands []string
	// Ref — эталон состояния; nil — ReferenceOf(Deps.Root).
	Ref *Reference
	// Tools — инструменты состояния вместо project.Tools (тесты).
	Tools []tools.Tool
	// Judge — судья-модель (отчётно); в день 31 не подключён.
	Judge bool
	// Log — ход прогона строками; nil — молча.
	Log io.Writer
}

// T14 гоняет стенды И-14 и возвращает результат каждого.
func T14(ctx context.Context, o T14Options) ([]*Result, error) {
	if o.LLM == nil {
		return nil, fmt.Errorf("нет модели")
	}
	qs, err := kb.LoadQuestions(o.Questions)
	if err != nil {
		return nil, err
	}
	reg := features.Catalog()
	stands := o.Stands
	if len(stands) == 0 {
		stands = []string{"docs", "state"}
	}
	var results []*Result
	for _, s := range stands {
		var r *Result
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "docs":
			r, err = t14Docs(ctx, o, reg, qs.Questions)
		case "state":
			r, err = t14State(ctx, o, reg, qs.Questions)
		default:
			return nil, fmt.Errorf("неизвестный стенд %q (docs, state)", s)
		}
		if err != nil {
			return results, err
		}
		if o.Judge {
			r.Notes = append(r.Notes, "судья-модель (-judge) в день 31 не подключён: все пороги — проверки кодом")
		}
		results = append(results, r)
	}
	return results, nil
}

// laneRun — ответы одной дорожки.
type laneRun struct {
	lane  Lane
	turns map[string]*help.Turn // по id вопроса
	rows  []Row
	cost  llm.Cost
	reqs  int
	steps int
	// constant — наибольшая постоянная часть запроса за дорожку.
	constant int
	secs     float64
}

// run — все вопросы по дорожке.
func run(ctx context.Context, o T14Options, lane Lane, qs []kb.Question, r *Result) (*laneRun, error) {
	lr := &laneRun{lane: lane, turns: map[string]*help.Turn{}}
	for _, q := range qs {
		started := time.Now()
		t, err := help.Run(ctx, o.LLM, o.Deps, q.Q, nil, help.Options{Model: o.Model, Features: lane.Features, Tools: o.Tools})
		if t == nil {
			return nil, fmt.Errorf("дорожка «%s», вопрос %s: %w", lane.Name, q.ID, err)
		}
		secs := time.Since(started).Seconds()
		lr.turns[q.ID] = t
		lr.cost = lr.cost.Add(t.Stats.Cost)
		lr.reqs += t.Stats.Steps
		lr.steps += t.Stats.Steps
		lr.secs += secs
		if c := t.Stats.Context.Estimate.Constant; c > lr.constant {
			lr.constant = c
		}
		row := Row{Lane: lane.Name, ID: q.ID, Type: q.Type, Split: q.Split, Question: q.Q,
			Status: t.Answer.Status, Answer: t.Answer.Answer, OK: !t.Unverified() && err == nil, Steps: t.Stats.Steps, Seconds: secs}
		if err != nil {
			row.Problems = append(row.Problems, err.Error())
			r.Notes = append(r.Notes, fmt.Sprintf("дорожка «%s», %s: ход оборвался: %v", lane.Name, q.ID, err))
		}
		row.Problems = append(row.Problems, t.Check.Problems...)
		row.Problems = append(row.Problems, t.StateProblems...)
		for _, c := range t.Calls.Calls() {
			if !c.Final {
				row.Calls = append(row.Calls, c.Tool)
			}
		}
		lr.rows = append(lr.rows, row)
		if o.Log != nil {
			fmt.Fprintf(o.Log, "  %-8s %-4s %-6s %d шаг. %s\n", lane.Name, q.ID, t.Answer.Status, t.Stats.Steps, cell(t.Answer.Answer, 70))
		}
	}
	return lr, nil
}

// t14Docs — стенд «docs»: вопросы docs и out, механизм help.rag.
func t14Docs(ctx context.Context, o T14Options, reg *features.Registry, all []kb.Question) (*Result, error) {
	base := reg.Defaults()
	lanes := []Lane{
		{Name: "main", Features: base, Note: "основная: база и инструменты"},
		{Name: "norag", Features: base.With(help.RAG, false), Note: "без базы документации"},
		{Name: "same", Features: base, Same: true},
	}
	mech, err := CheckLanes(reg, lanes)
	if err != nil {
		return nil, err
	}
	var qs []kb.Question
	for _, q := range all {
		if q.Type == TypeDocs || q.Type == TypeOut {
			qs = append(qs, q)
		}
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("в наборе нет вопросов docs/out")
	}
	r := &Result{Trial: Trial14, Stand: "docs", Mechanism: mech, Model: o.Model, Started: time.Now(), Lanes: lanes}
	if o.Log != nil {
		fmt.Fprintf(o.Log, "%s · стенд docs: %d вопросов × %d дорожек\n", Trial14, len(qs), len(lanes))
	}
	runs := map[string]*laneRun{}
	for _, l := range lanes {
		lr, err := run(ctx, o, l, qs, r)
		if err != nil {
			return r, err
		}
		runs[l.Name] = lr
		r.Cost = r.Cost.Add(lr.cost)
		r.Requests += lr.reqs
		r.Rows = append(r.Rows, lr.rows...)
	}
	r.Seconds = time.Since(r.Started).Seconds()

	type tally struct {
		testN, cited, mustTest, falseUnknown, devN, mustDev, outN, outUnknown, unverified, n int
	}
	count := func(lr *laneRun) tally {
		var t tally
		for _, q := range qs {
			tr := lr.turns[q.ID]
			t.n++
			if tr.Unverified() {
				t.unverified++
			}
			a := tr.Answer
			switch {
			case q.Type == TypeOut:
				t.outN++
				if a.Unknown() {
					t.outUnknown++
				}
			case q.Split == kb.SplitTest:
				t.testN++
				if a.Unknown() {
					t.falseUnknown++
					continue
				}
				ck := tr.Check
				if ck.OK && !ck.Unverified && ck.HasSources && ck.HasQuotes && ck.Verbatim >= 1 {
					t.cited++
				}
				if MustOK(a.Answer, q.Expect) {
					t.mustTest++
				}
			case q.Split == kb.SplitDev:
				t.devN++
				if !a.Unknown() && MustOK(a.Answer, q.Expect) {
					t.mustDev++
				}
			}
		}
		return t
	}
	tl := map[string]tally{}
	for name, lr := range runs {
		tl[name] = count(lr)
	}
	m, same, norag := tl["main"], tl["same"], tl["norag"]
	mr := runs["main"]
	r.check("источники и дословные цитаты (test)", "main", m.cited == m.testN, ratio(m.cited, m.testN), ratio(m.testN, m.testN))
	needMust := m.testN - 1
	r.check("must (test)", "main", m.mustTest >= needMust, ratio(m.mustTest, m.testN), "≥ "+ratio(needMust, m.testN))
	needOut := int(math.Ceil(0.75 * float64(m.outN)))
	r.check("out → unknown", "main", m.outUnknown >= needOut, ratio(m.outUnknown, m.outN), "≥ "+ratio(needOut, m.outN))
	r.check("ложных unknown (test)", "main", m.falseUnknown <= MaxFalseUnknown, strconv.Itoa(m.falseUnknown), fmt.Sprintf("≤ %d", MaxFalseUnknown))
	r.check("«не проверено»", "main", m.unverified == 0, strconv.Itoa(m.unverified), "0")
	avg := float64(mr.steps) / float64(max(m.n, 1))
	r.check("запросов на ход (среднее)", "main", avg <= MaxStepsAvg, trimFloat(avg), fmt.Sprintf("≤ %s", trimFloat(MaxStepsAvg)))
	r.check("постоянная часть запроса", "main", mr.constant <= MaxConstant, strconv.Itoa(mr.constant)+" токенов", fmt.Sprintf("≤ %d", MaxConstant))
	// Контроль «без базы»: инструменты файлов остаются (read_file,
	// repo_search), поэтому точность может и не упасть — агент читает те же
	// документы напрямую. Что даёт механизм help.rag — проверяемые источники
	// и дешёвый ход: без него источников нет, а запросов на ход больше, чем
	// шум между повторами основной дорожки.
	r.check("без базы: ответов с источниками и цитатами", "norag", norag.cited <= 1,
		ratio(norag.cited, norag.testN), "≤ 1")
	avgNorag := float64(runs["norag"].steps) / float64(max(norag.n, 1))
	avgSame := float64(runs["same"].steps) / float64(max(same.n, 1))
	stepNoise := math.Abs(avg - avgSame)
	r.check("без базы: запросов на ход больше шума", "norag", avgNorag-avg > stepNoise,
		fmt.Sprintf("norag %s, main %s, повтор %s (шум %s)", trimFloat(avgNorag), trimFloat(avg), trimFloat(avgSame), trimFloat(stepNoise)), "рост > шум")
	r.metric("must (test): падение без базы", "norag", float64(m.mustTest-norag.mustTest), "", fmt.Sprintf("шум %d", abs(m.mustTest-same.mustTest)))
	for _, name := range sortedKeys(tl) {
		t, lr := tl[name], runs[name]
		r.metric("must (test)", name, float64(t.mustTest), "", ratio(t.mustTest, t.testN))
		r.metric("must (dev)", name, float64(t.mustDev), "", ratio(t.mustDev, t.devN)+" — калибровка порога")
		r.metric("источники и цитаты (test)", name, float64(t.cited), "", ratio(t.cited, t.testN))
		r.metric("out → unknown", name, float64(t.outUnknown), "", ratio(t.outUnknown, t.outN))
		r.metric("ложных unknown (test)", name, float64(t.falseUnknown), "", "")
		r.metric("«не проверено»", name, float64(t.unverified), "", "")
		r.metric("запросов на ход", name, float64(lr.steps)/float64(max(t.n, 1)), "", "")
		r.metric("постоянная часть", name, float64(lr.constant), "токенов", "наибольшая за дорожку")
		r.metric("цена", name, lr.cost.USD, "$", lr.cost.Tariff)
		r.metric("время", name, lr.secs, "с", "")
	}
	return r, nil
}

// t14State — стенд «state»: вопросы о состоянии, механизм help.tools.
func t14State(ctx context.Context, o T14Options, reg *features.Registry, all []kb.Question) (*Result, error) {
	base := reg.Defaults()
	lanes := []Lane{
		{Name: "main", Features: base, Note: "основная: база и инструменты"},
		{Name: "notools", Features: base.With(help.Tools, false), Note: "без инструментов состояния"},
		{Name: "same", Features: base, Same: true},
	}
	mech, err := CheckLanes(reg, lanes)
	if err != nil {
		return nil, err
	}
	var qs []kb.Question
	for _, q := range all {
		if q.Type == TypeState {
			qs = append(qs, q)
		}
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("в наборе нет вопросов state")
	}
	ref := o.Ref
	if ref == nil {
		rf, err := ReferenceOf(ctx, o.Deps.Root, qs)
		if err != nil {
			return nil, fmt.Errorf("эталон состояния: %w", err)
		}
		ref = &rf
	}
	r := &Result{Trial: Trial14, Stand: "state", Mechanism: mech, Model: o.Model, Started: time.Now(), Lanes: lanes}
	r.Notes = append(r.Notes, fmt.Sprintf("эталон кода: ветка %q, не done %v, заданий %d, прогоны %v", ref.Branch, ref.NotDone, ref.Count, ref.LastRun))
	if o.Log != nil {
		fmt.Fprintf(o.Log, "%s · стенд state: %d вопросов × %d дорожек; эталон: ветка %s, не done %v, заданий %d\n",
			Trial14, len(qs), len(lanes), ref.Branch, ref.NotDone, ref.Count)
	}
	runs := map[string]*laneRun{}
	for _, l := range lanes {
		lr, err := run(ctx, o, l, qs, r)
		if err != nil {
			return r, err
		}
		runs[l.Name] = lr
		r.Cost = r.Cost.Add(lr.cost)
		r.Requests += lr.reqs
		r.Rows = append(r.Rows, lr.rows...)
	}
	r.Seconds = time.Since(r.Started).Seconds()

	type tally struct{ n, fromTool, equal, unverified int }
	count := func(lr *laneRun) (tally, bool) {
		var t tally
		branchMissing := false
		for _, q := range qs {
			tr := lr.turns[q.ID]
			t.n++
			if tr.Unverified() {
				t.unverified++
			}
			if FromTool(tr) {
				t.fromTool++
			}
			ok, _ := EqualRef(tr, q, *ref)
			if ok {
				t.equal++
			}
			if kind, _ := refKind(q); kind == "branch" && !ok {
				branchMissing = true
			}
		}
		return t, branchMissing
	}
	tl := map[string]tally{}
	missing := map[string]bool{}
	for name, lr := range runs {
		tl[name], missing[name] = count(lr)
	}
	m, nt := tl["main"], tl["notools"]
	mr := runs["main"]
	r.check("state: факт из инструмента", "main", m.fromTool == m.n, ratio(m.fromTool, m.n), ratio(m.n, m.n))
	r.check("state: равен эталону кода", "main", m.equal == m.n, ratio(m.equal, m.n), ratio(m.n, m.n))
	r.check("«не проверено»", "main", m.unverified == 0, strconv.Itoa(m.unverified), "0")
	avg := float64(mr.steps) / float64(max(m.n, 1))
	r.check("запросов на ход (среднее)", "main", avg <= MaxStepsAvg, trimFloat(avg), fmt.Sprintf("≤ %s", trimFloat(MaxStepsAvg)))
	r.check("постоянная часть запроса", "main", mr.constant <= MaxConstant, strconv.Itoa(mr.constant)+" токенов", fmt.Sprintf("≤ %d", MaxConstant))
	r.check("без инструментов: ветка отсутствует или факт не из инструмента", "notools", nt.fromTool == 0 || missing["notools"],
		fmt.Sprintf("из инструмента %s, ветка не названа: %v", ratio(nt.fromTool, nt.n), missing["notools"]), "0 из инструмента или ветки нет")
	for _, name := range sortedKeys(tl) {
		t, lr := tl[name], runs[name]
		r.metric("факт из инструмента", name, float64(t.fromTool), "", ratio(t.fromTool, t.n))
		r.metric("равен эталону", name, float64(t.equal), "", ratio(t.equal, t.n))
		r.metric("«не проверено»", name, float64(t.unverified), "", "")
		r.metric("запросов на ход", name, float64(lr.steps)/float64(max(t.n, 1)), "", "")
		r.metric("постоянная часть", name, float64(lr.constant), "токенов", "наибольшая за дорожку")
		r.metric("цена", name, lr.cost.USD, "$", lr.cost.Tariff)
		r.metric("время", name, lr.secs, "с", "")
	}
	return r, nil
}

// FromTool — у ответа есть хотя бы один факт о состоянии, подтверждённый
// вызовом инструмента состояния этого хода, и ответ принят без пометки.
func FromTool(t *help.Turn) bool {
	if t == nil || t.Answer.Unknown() || t.StateUnverified {
		return false
	}
	for _, f := range t.Answer.StateFacts {
		c, ok := t.Calls.Get(f.CallID)
		if ok && help.IsStateTool(c.Tool) && !c.Error && !c.Final {
			return true
		}
	}
	return false
}

// EqualRef — ответ на вопрос о состоянии совпадает с эталоном кода. Второе
// значение — почему нет.
func EqualRef(t *help.Turn, q kb.Question, ref Reference) (bool, string) {
	if t == nil {
		return false, "нет ответа"
	}
	text := t.Answer.Answer
	for _, f := range t.Answer.StateFacts {
		text += "\n" + f.Fact
	}
	kind, num := refKind(q)
	if t.Answer.Unknown() {
		if kind == "last-run" {
			if lr, ok := ref.LastRun[num]; !ok || !lr.Has {
				return true, ""
			}
		}
		return false, "ответ unknown"
	}
	switch kind {
	case "branch":
		if ref.Branch == "" {
			return false, "эталон ветки пуст"
		}
		if !strings.Contains(corpus.Normalize(text), corpus.Normalize(ref.Branch)) {
			return false, fmt.Sprintf("в ответе нет ветки %q", ref.Branch)
		}
		return true, ""
	case "not-done":
		for _, n := range ref.NotDone {
			if !hasNumber(text, n) {
				return false, fmt.Sprintf("в ответе нет задания %s", n)
			}
		}
		return true, ""
	case "count":
		if !hasNumber(text, strconv.Itoa(ref.Count)) {
			return false, fmt.Sprintf("в ответе нет числа %d", ref.Count)
		}
		return true, ""
	case "last-run":
		lr, ok := ref.LastRun[num]
		if !ok || !lr.Has {
			low := strings.ToLower(text)
			if strings.Contains(low, "нет") || strings.Contains(low, "не было") {
				return true, ""
			}
			return false, "прогона нет, а ответ его описывает"
		}
		if !hasNumber(text, strconv.Itoa(lr.ChecksOK)) || !hasNumber(text, strconv.Itoa(lr.Checks)) {
			return false, fmt.Sprintf("в ответе нет проверок %d/%d", lr.ChecksOK, lr.Checks)
		}
		low := strings.ToLower(text)
		failed := strings.Contains(low, "не пройден") || strings.Contains(low, "провал") || strings.Contains(low, "passed: false") || strings.Contains(low, "не прошёл") || strings.Contains(low, "не прошел")
		if lr.Passed == failed {
			return false, fmt.Sprintf("прогон passed=%v, а ответ говорит иначе", lr.Passed)
		}
		return true, ""
	}
	return false, "у вопроса нет note «ref:…»"
}

// MustOK — в ответе есть хотя бы одна форма из каждой группы must
// (после нормализации); пустой expect — всегда да.
func MustOK(text string, e *kb.Expect) bool {
	if e == nil {
		return true
	}
	t := corpus.Normalize(text)
	for _, group := range e.Must {
		found := false
		for _, form := range group {
			if strings.Contains(t, corpus.Normalize(form)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, bad := range e.MustNot {
		if strings.Contains(t, corpus.Normalize(bad)) {
			return false
		}
	}
	return true
}

// hasNumber — число как отдельный токен («3» не находится в «30»).
func hasNumber(text, n string) bool {
	n = strings.TrimLeft(n, "0")
	if n == "" {
		n = "0"
	}
	re := regexp.MustCompile(`(^|\D)0*` + regexp.QuoteMeta(n) + `(\D|$)`)
	return re.MatchString(text)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
