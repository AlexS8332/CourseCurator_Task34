package help

import (
	"strings"
	"sync"

	"aitr/34-files-agent/internal/agent"
)

// Call — один вызов инструмента в ходе: что модель (или код до первого
// запроса) попросила и что получила. По CallID на него ссылаются
// state_facts ответа: «ветка main — из вызова call_3_git_branch».
type Call struct {
	Tool   string  `json:"tool"`
	CallID string  `json:"call_id"`
	Step   int     `json:"step"`
	Args   string  `json:"args"`
	Result string  `json:"result"`
	Error  bool    `json:"error,omitempty"`
	Secs   float64 `json:"seconds,omitempty"`
	// Final — вызов завершающего инструмента (help_answer): в журнале
	// вызовов он есть, но ссылаться на него как на источник факта нельзя.
	Final bool `json:"final,omitempty"`
}

// CallLog — журнал вызовов инструментов одного хода. Заполняется событиями
// agent.Event (реализует agent.Emitter, поэтому подключается к Runner через
// agent.Tee рядом с журналом окна) — а не словами модели: проверка
// «факт из инструмента» сверяется с тем, что инструмент вернул на самом деле
// (ИП-1). Безопасен для вызова из нескольких горутин.
type CallLog struct {
	mu    sync.Mutex
	calls []Call
	byID  map[string]int
}

// Log разбирает события вызовов: tool.call открывает запись, tool.result и
// tool.error закрывают её результатом. Остальные события журнала не нужны.
func (l *CallLog) Log(e agent.Event) {
	if l == nil || e.CallID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID == nil {
		l.byID = map[string]int{}
	}
	switch e.Kind {
	case agent.EventToolCall:
		if _, dup := l.byID[e.CallID]; dup {
			return
		}
		l.byID[e.CallID] = len(l.calls)
		l.calls = append(l.calls, Call{Tool: e.Tool, CallID: e.CallID, Step: e.Step, Args: e.Detail, Final: e.Final})
	case agent.EventToolResult, agent.EventToolError:
		i, ok := l.byID[e.CallID]
		if !ok {
			return
		}
		c := &l.calls[i]
		// Результат завершающего инструмента в журнале — «принят/не принят»,
		// а не данные; у обычного вызова Detail — ответ инструмента (для
		// длинных — обрезанный Runner'ом, см. Result).
		if e.Kind == agent.EventToolError {
			c.Error = true
		}
		if !c.Final {
			c.Result = e.Detail
			if e.Result != "" {
				c.Result = e.Result
			}
		} else if e.Kind == agent.EventToolError {
			c.Result = e.Detail
		}
		c.Secs = e.Seconds
	}
}

// Publish — промежуточные результаты журналу вызовов не нужны.
func (l *CallLog) Publish(agent.Update) {}

// Get — вызов по id.
func (l *CallLog) Get(id string) (Call, bool) {
	if l == nil {
		return Call{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	i, ok := l.byID[strings.TrimSpace(id)]
	if !ok {
		return Call{}, false
	}
	return l.calls[i], true
}

// Calls — копия журнала по порядку вызовов.
func (l *CallLog) Calls() []Call {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Call(nil), l.calls...)
}

// Of — вызовы одного инструмента.
func (l *CallLog) Of(tool string) []Call {
	var out []Call
	for _, c := range l.Calls() {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// StateCalls — удавшиеся вызовы инструментов состояния (всё, кроме
// docs_search и завершающего): на них можно ссылаться в state_facts.
func (l *CallLog) StateCalls() []Call {
	var out []Call
	for _, c := range l.Calls() {
		if IsStateTool(c.Tool) && !c.Final && !c.Error {
			out = append(out, c)
		}
	}
	return out
}

// IsStateTool — инструмент состояния: любой инструмент проекта, кроме
// поиска по документации (git_*, assignments_*, assignment_get,
// demo_report, repo_*, read_file, aitr_doctor).
func IsStateTool(name string) bool {
	return name != "" && name != DocsTool && name != FinishName
}
