// Package help — агент «/help»: ассистент каталога курса (день 31).
//
// Один ход, один агент, два рода фактов. Факты об устройстве курса
// (регламент, карточки, выводы прошлых заданий) — только из docs_search с
// источниками и дословными цитатами, которые проверяет cite.CheckWith.
// Факты о состоянии (ветка, статусы, файлы, прогоны) — только из
// инструментов проекта (git_*, assignments_*, demo_report …), и каждый
// такой факт в ответе ссылается на call_id вызова, результат которого его
// содержит (StateProblems). Нет оснований — только unknown с уточняющим
// вопросом. Закончить ход модель может лишь завершающим help_answer.
//
// Механизмы (features.Catalog): help.rag — docs_search и его вызов кодом
// до первого запроса с текстом вопроса (как rag.Hook в v30); help.tools —
// инструменты состояния. Выключенный механизм не дыра: без базы ответ
// принимается без источников (сравнение «с базой — без базы» в И-14), без
// инструментов факты о состоянии проверить нечем — и ответ на вопрос о
// состоянии либо unknown, либо отказ «не из инструмента».
package help

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/tools"
)

const (
	// Name — имя агента в журнале.
	Name = "help"
	// FinishName — завершающий инструмент.
	FinishName = "help_answer"
	// DocsTool — поиск по документации курса (cite.SearchTool).
	DocsTool = "docs_search"
	// MaxSteps — предел запросов к модели за ход.
	MaxSteps = 8
	// DocsK — сколько фрагментов просит вызов docs_search кодом.
	DocsK = 6
	// Role — роль инструментов проекта для этого агента.
	Role = "course"

	// RAG и Tools — механизмы агента в реестре features.Catalog.
	RAG   = features.HelpRAG
	Tools = features.HelpTools
)

// StateFact — факт о состоянии и вызов, из результата которого он взят.
type StateFact struct {
	Fact   string `json:"fact"`
	CallID string `json:"call_id"`
}

// Answer — аргументы help_answer: ответ с источниками и цитатами (cite.Cited)
// плюс факты о состоянии.
type Answer struct {
	cite.Cited
	StateFacts []StateFact `json:"state_facts,omitempty"`
}

// Result — принятый ответ: что проверил код и выдача хода (для заголовков
// источников в окне). Результат Handle завершающего инструмента.
type Result struct {
	Answer Answer         `json:"answer"`
	Check  cite.CiteCheck `json:"check"`
	Hits   []kb.Hit       `json:"-"`
	// StateProblems — проблемы фактов о состоянии у принятого ответа;
	// StateUnverified — принят после MaxRejects отказов, проверку не прошёл.
	StateProblems   []string `json:"state_problems,omitempty"`
	StateUnverified bool     `json:"state_unverified,omitempty"`
}

// Text — ответ человеку в markdown (cite.CitedResult.Text) с пометкой о
// непроверенных фактах состояния.
func (r *Result) Text() string {
	if r == nil {
		return ""
	}
	cr := &cite.CitedResult{Cited: r.Answer.Cited, Check: r.Check, Hits: r.Hits}
	s := cr.Text()
	if r.StateUnverified {
		s += "\n\n_Факты о состоянии не проверены: " + strings.Join(r.StateProblems, "; ") + "._"
	}
	return s
}

// Unverified — ответ принят без подтверждения (цитаты или факты состояния).
func (r *Result) Unverified() bool {
	return r != nil && (r.Check.Unverified || r.StateUnverified)
}

// Turn — итог хода для окна, CLI и стенда.
type Turn struct {
	Question string         `json:"question"`
	Answer   Answer         `json:"answer"`
	Check    cite.CiteCheck `json:"check"`
	Stats    agent.Stats    `json:"stats"`
	Text     string         `json:"text"`
	// Features — набор механизмов хода (после отката: базы нет → help.rag
	// выключен).
	Features        features.Set `json:"features"`
	StateProblems   []string     `json:"state_problems,omitempty"`
	StateUnverified bool         `json:"state_unverified,omitempty"`
	// Calls — журнал вызовов хода; Hits — выдача docs_search хода.
	Calls *CallLog `json:"-"`
	Hits  []kb.Hit `json:"-"`
}

// Unverified — хоть одна проверка не прошла, ответ принят с пометкой.
func (t *Turn) Unverified() bool {
	return t != nil && (t.Check.Unverified || t.StateUnverified)
}

// Options — чем ход отличается от умолчаний.
type Options struct {
	// Model — модель для Runner; пусто — llm.DefaultModel.
	Model string
	// Features — набор механизмов; пустой — умолчания реестра.
	Features    features.Set
	Temperature float64
	// Tools — инструменты состояния вместо project.Tools(d): для тестов
	// без каталога курса.
	Tools []tools.Tool
}

// System — системный промпт агента. Короткий: постоянная часть запроса
// (промпт + описания инструментов) укладывается в бюджет 6 тыс. токенов.
const System = `Ты — ассистент каталога учебного курса по AI. Каталог: задания assignments/NN-slug (каждое — свой git-репозиторий с assignment.json), карточки заданий docs/cards/NN-slug.md, конспекты docs/notes, пособия docs/summ, инструментарий aitr (scripts/aitr.ps1, скилл .claude/skills/aitr) и edgerec (tools/edgerec — контрольный прогон в Edge с записью видео), сценарии прогонов demos/NN-slug. Отвечаешь автору курса на вопросы о том, как каталог устроен и что в нём сейчас.

Правила:
1. Факты об устройстве курса, регламенте, прошлых заданиях и их выводах — только из результатов docs_search. Каждый такой факт подтверждай источником (sources: chunk_id) и дословной цитатой из найденного фрагмента (quotes: chunk_id, text — скопированный кусок текста, не пересказ, не короче одной фразы). Чего нет в выдаче — не утверждай по памяти.
2. Факты о текущем состоянии — ветка, статусы заданий, список и содержимое файлов, изменения, последний прогон — только из инструментов состояния (git_*, assignments_list, assignment_get, demo_report, repo_tree, repo_search, read_file). Каждый такой факт перечисли в state_facts: fact — факт словами, call_id — id вызова, в результате которого он есть. Имена веток, статусы, пути и числа переноси из результата дословно.
3. Нет оснований ни в документах, ни в инструментах (вопрос не о курсе, выдача не по теме) — status unknown: answer — одна фраза, почему ответа нет, clarify — уточняющий вопрос. Общие знания не в счёт.
4. Ход заканчивается только вызовом help_answer; ответ текстом не принимается. Отказ help_answer объясняет, что исправить — исправь и вызови снова.
5. Экономь запросы: первый docs_search уже сделан кодом с текстом вопроса — повторный поиск нужен только с другим запросом; инструмент состояния — один вызов на факт. Отвечай по-русски, кратко и по существу; в answer не повторяй цитаты.`

// finishSchema — схема help_answer: поля kb_answer плюс state_facts.
const finishSchema = `{"type":"object","properties":{
"status":{"type":"string","enum":["answered","unknown"],"description":"answered — есть опора в документах или инструментах; unknown — ответа нет"},
"answer":{"type":"string","description":"ответ человеку по-русски; у unknown — почему ответа нет"},
"sources":{"type":"array","items":{"type":"object","properties":{"chunk_id":{"type":"string"}},"required":["chunk_id"]},"description":"фрагменты docs_search этого хода, на которые опирается ответ"},
"quotes":{"type":"array","items":{"type":"object","properties":{"chunk_id":{"type":"string"},"text":{"type":"string"}},"required":["chunk_id","text"]},"description":"дословные цитаты из этих фрагментов"},
"state_facts":{"type":"array","items":{"type":"object","properties":{"fact":{"type":"string"},"call_id":{"type":"string"}},"required":["fact","call_id"]},"description":"факты о состоянии и call_id вызова инструмента, в результате которого факт есть"},
"clarify":{"type":"string","description":"уточняющий вопрос человеку; обязателен при unknown"}
},"required":["status","answer"]}`

const finishDescription = "Заверши ход: ответ с опорой. Факт из документации — sources (chunk_id из docs_search этого хода) и quotes (дословные цитаты); " +
	"факт о состоянии — state_facts с call_id вызова инструмента. Нет опоры — status unknown с clarify. " +
	"Код сверяет chunk_id с выдачей, цитаты с текстом фрагментов, ветки/статусы/пути/числа — с результатами названных вызовов; не прошло — отказ с объяснением."

// Spec — описание агента для agent.Runner с умолчаниями реестра: имя help,
// инструменты роли course проекта плюс docs_search над выдачей obs,
// завершающий help_answer, MaxSteps 8.
func Spec(d project.Deps, obs *cite.Issued, calls *CallLog) agent.Spec {
	return SpecWith(d, obs, calls, "", Options{})
}

// SpecWith — Spec с вопросом (его числа в цитатах не нужны) и настройками.
// obs == nil — без docs_search (механизм help.rag выключен или базы нет);
// help.tools выключен — без инструментов состояния.
func SpecWith(d project.Deps, obs *cite.Issued, calls *CallLog, question string, o Options) agent.Spec {
	fs := o.Features
	if fs.Empty() {
		fs = features.Catalog().Defaults()
	}
	var ts []tools.Tool
	if fs.On(Tools) {
		state := o.Tools
		if state == nil {
			state = project.Role(project.Tools(d), Role)
		}
		for _, t := range state {
			if t.Spec().Name != DocsTool {
				ts = append(ts, t)
			}
		}
	}
	if obs != nil && fs.On(RAG) && d.Searcher != nil {
		ts = append(ts, cite.SearchTool(d.Searcher, d.Index, DocsK, obs))
	}
	return agent.Spec{
		Name:     Name,
		System:   System,
		Tools:    ts,
		Finish:   []agent.Finisher{FinisherFor(obs, calls, question)},
		MaxSteps: MaxSteps,
	}
}

// Finisher — help_answer: cite.FinisherOf (chunk_id из выдачи хода, дословные
// цитаты, числа в цитатах, Gate) плюс проверка state_facts.
func Finisher(obs *cite.Issued, calls *CallLog) agent.Finisher {
	return FinisherFor(obs, calls, "")
}

// FinisherFor — Finisher с вопросом хода.
//
// Ответ о состоянии (есть state_facts, нет источников и цитат) проверку
// цитат не проходит — ему нечего цитировать; ответ с источниками или
// unknown идёт через cite целиком, в том числе через Gate: пустая выдача
// без вызовов состояния — только unknown. obs == nil (без базы) — цитаты
// не проверяются, ответ принимается как есть. Факты о состоянии
// проверяются всегда; после cite.MaxRejects отказов ответ принимается с
// пометкой StateUnverified (иначе ход крутился бы до предела шагов).
func FinisherFor(obs *cite.Issued, calls *CallLog, question string) agent.Finisher {
	var inner *agent.Finisher
	if obs != nil {
		f := cite.FinisherOf(cite.FinishOptions{Hits: obs.Hits, Gate: obs.Gate, Question: question})
		inner = &f
	}
	var mu sync.Mutex
	stateRejects := 0
	return agent.Finisher{
		Name:        FinishName,
		Description: finishDescription,
		Parameters:  json.RawMessage(finishSchema),
		Handle: func(ctx context.Context, callID string, args json.RawMessage) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			var a Answer
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, fmt.Errorf("аргументы help_answer не разобраны (%v): передай JSON {status, answer, sources, quotes, state_facts, clarify}", err)
			}
			a.Status = strings.ToLower(strings.TrimSpace(a.Status))
			res := &Result{}
			// Ответ о состоянии: без источников и цитат, со state_facts или с
			// упоминанием ветки/статуса/пути — цитировать ему нечего, проверка
			// идёт по инструментам (и объясняет, какой вызвать).
			stateOnly := len(a.Sources) == 0 && len(a.Quotes) == 0 && a.Status == cite.StatusAnswered &&
				(len(a.StateFacts) > 0 || HasStateMentions(a.Answer))
			switch {
			case inner != nil && !stateOnly:
				out, err := inner.Handle(ctx, callID, args)
				if err != nil {
					if len(calls.StateCalls()) > 0 && len(a.StateFacts) == 0 {
						err = fmt.Errorf("%w; факт о состоянии (ветка, статус, файлы, прогон) подтверждай не цитатой, а state_facts[{fact, call_id}] с id своего вызова инструмента", err)
					}
					return nil, err
				}
				cr, ok := out.(*cite.CitedResult)
				if !ok {
					return nil, errors.New("внутренняя ошибка: неожиданный результат проверки цитат")
				}
				a.Cited = cr.Cited
				res.Check, res.Hits = cr.Check, cr.Hits
			default:
				// Без базы или ответ только о состоянии: проверять цитаты нечем
				// и незачем; unknown без уточнения получает вопрос по умолчанию.
				res.Check = cite.CiteCheck{OK: true, Grounded: true,
					HasSources: len(a.Sources) > 0, HasQuotes: len(a.Quotes) > 0}
				if a.Unknown() && strings.TrimSpace(a.Clarify) == "" {
					a.Clarify = "Уточните, пожалуйста, о каком задании, файле или части каталога идёт речь?"
				}
				if obs != nil {
					res.Hits = obs.Hits()
				}
			}
			if !a.Unknown() {
				if problems := StateProblems(a, calls, question); len(problems) > 0 {
					if stateRejects < cite.MaxRejects {
						stateRejects++
						return nil, fmt.Errorf("ответ не принят: %s", strings.Join(problems, "; "))
					}
					res.StateProblems, res.StateUnverified = problems, true
				}
			}
			res.Answer = a
			return res, nil
		},
	}
}

// Run — один ход агента: вопрос → ответ с проверками. em — журнал окна
// (nil — без журнала); журнал вызовов хода ведётся всегда. Ошибка хода
// (протокол, предел шагов, модель) возвращается вместе с Turn, где ответ —
// «не знаю» с пометкой «не проверено» и ближайшим найденным.
func Run(ctx context.Context, cl llm.Chatter, d project.Deps, q string, em agent.Emitter, o Options) (*Turn, error) {
	q = Clean(q)
	if q == "" {
		return nil, errors.New("пустой вопрос")
	}
	if em == nil {
		em = agent.Nop{}
	}
	fs := o.Features
	if fs.Empty() {
		fs = features.Catalog().Defaults()
	}
	model := o.Model
	if model == "" {
		model = llm.DefaultModel
	}
	var obs *cite.Issued
	switch {
	case !fs.On(RAG):
		em.Log(agent.Event{Agent: Name, Kind: agent.EventMechanism, Mechanism: string(RAG),
			Title: "база документации выключена: docs_search не выдан, ответ без источников"})
	case d.Searcher == nil:
		fs = fs.With(RAG, false)
		em.Log(agent.Event{Agent: Name, Kind: agent.EventMechanism, Mechanism: string(RAG),
			Title: "базы документации нет — откат: ход без docs_search (соберите индекс: curator index)"})
	default:
		obs = &cite.Issued{}
	}
	if !fs.On(Tools) {
		em.Log(agent.Event{Agent: Name, Kind: agent.EventMechanism, Mechanism: string(Tools),
			Title: "инструменты состояния выключены: ветку, статусы и прогоны агенту взять неоткуда"})
	}
	calls := &CallLog{}
	spec := SpecWith(d, obs, calls, q, o)
	prep := agent.Prepared{User: q, Features: fs}
	if obs != nil {
		args, _ := json.Marshal(struct {
			Query string `json:"query"`
			K     int    `json:"k"`
		}{q, DocsK})
		prep.Preload = []agent.Preload{{Tool: DocsTool, Args: string(args)}}
		em.Log(agent.Event{Agent: Name, Kind: agent.EventMechanism, Mechanism: string(RAG), Tool: DocsTool,
			Title: fmt.Sprintf("документация: вызов docs_search кодом до первого запроса (k %d)", DocsK)})
	}
	runner := agent.Runner{LLM: cl, Model: model, Temperature: o.Temperature}
	reply, err := runner.Run(ctx, spec, prep, agent.Tee{calls, em})
	t := &Turn{Question: q, Stats: reply.Stats, Features: fs, Calls: calls}
	if obs != nil {
		t.Hits = obs.Hits()
	}
	if err != nil {
		var rel []kb.Hit
		if obs != nil {
			_, _, rel = obs.Gate()
		}
		ur := cite.UnverifiedResult(err, t.Hits, rel)
		t.Answer, t.Check, t.Text = Answer{Cited: ur.Cited}, ur.Check, ur.Text()
		return t, err
	}
	res, ok := reply.Final.(*Result)
	if !ok {
		err := errors.New("ход закончился без help_answer")
		ur := cite.UnverifiedResult(err, t.Hits, nil)
		t.Answer, t.Check, t.Text = Answer{Cited: ur.Cited}, ur.Check, ur.Text()
		return t, err
	}
	t.Answer, t.Check, t.Text = res.Answer, res.Check, res.Text()
	t.StateProblems, t.StateUnverified = res.StateProblems, res.StateUnverified
	if len(res.Hits) > 0 {
		t.Hits = res.Hits
	}
	return t, nil
}

// Ask — Run с умолчаниями (модель llm.DefaultModel, механизмы реестра).
func Ask(ctx context.Context, cl llm.Chatter, d project.Deps, q string, em agent.Emitter) (Answer, cite.CiteCheck, agent.Stats, error) {
	t, err := Run(ctx, cl, d, q, em, Options{})
	if t == nil {
		return Answer{}, cite.CiteCheck{}, agent.Stats{}, err
	}
	return t.Answer, t.Check, t.Stats, err
}

// Clean — текст вопроса из реплики: «/help …» и просто текст — одно и то же.
func Clean(q string) string {
	q = strings.TrimSpace(q)
	for _, p := range []string{"/help", "/Help", "/HELP"} {
		if strings.HasPrefix(q, p) {
			rest := q[len(p):]
			if rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\n") {
				q = strings.TrimSpace(rest)
			}
			break
		}
	}
	return q
}
