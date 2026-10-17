package help

import (
	"fmt"
	"regexp"
	"strings"

	"aitr/34-files-agent/internal/corpus"
)

// Проверка фактов о состоянии (ветка, статусы, пути, числа) кодом.
//
// Модель может сказать «корень на ветке main» только если какой-то
// инструмент этого хода вернул «main» — и ответ обязан назвать этот вызов
// в state_facts по call_id. Проверяется не слово модели, а подстрока
// реального результата инструмента (ИП-1). Регэкспы узкие: ложный отказ
// учит сослаться на инструмент, а не ломает ход (ИП-8: каждый отказ
// говорит, что не так и что сделать).

// Mention — упоминание в тексте ответа, которому нужна опора.
type Mention struct {
	Kind string // branch | status | path | number
	Text string
}

var (
	// branchRe — имя ветки после слова «ветка/ветке/ветки/ветку/branch»:
	// `[a-z0-9/_.-]+`, с необязательной кавычкой или обратным апострофом.
	branchRe = regexp.MustCompile(`(?i)(?:ветк[а-яё]*|branch)\s*[:—-]?\s*[«"'\x60]?([a-z0-9][a-z0-9/_.-]*)`)
	statusRe = regexp.MustCompile(`\b(planned|in-progress|done|blocked)\b`)
	pathRe   = regexp.MustCompile(`\b(?:docs|demos|assignments)/[\w.\-/]+`)
	numberRe = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
)

// Mentions — упоминания в тексте: ветки, статусы, пути и числа. Числа
// внутри путей и имён веток отдельно не считаются — путь проверяется
// целиком.
func Mentions(text string) []Mention {
	var out []Mention
	seen := map[string]bool{}
	add := func(kind, t string) {
		t = strings.TrimRight(strings.TrimSpace(t), ".,;:)»\"'`")
		if t == "" || seen[kind+"\x00"+t] {
			return
		}
		seen[kind+"\x00"+t] = true
		out = append(out, Mention{Kind: kind, Text: t})
	}
	covered := text
	for _, m := range branchRe.FindAllStringSubmatch(text, -1) {
		add("branch", m[1])
		covered = strings.Replace(covered, m[1], " ", 1)
	}
	for _, m := range pathRe.FindAllString(text, -1) {
		add("path", m)
		covered = strings.ReplaceAll(covered, m, " ")
	}
	for _, m := range statusRe.FindAllString(text, -1) {
		add("status", m)
	}
	for _, m := range numberRe.FindAllString(covered, -1) {
		add("number", m)
	}
	return out
}

// HasStateMentions — в тексте названы ветка, статус или путь: это ответ о
// состоянии, и опора ему — инструмент, а не цитата.
func HasStateMentions(text string) bool {
	for _, m := range Mentions(text) {
		if m.Kind != "number" {
			return true
		}
	}
	return false
}

// norm — нормализация для сравнения: регистр, ё/е, кавычки, пробелы
// (corpus.Normalize) — та же, что у цитат.
func norm(s string) string { return corpus.Normalize(s) }

// contains — есть ли упоминание в тексте. Число ищется как отдельный токен
// («3» не находится в «30»), остальное — подстрокой после нормализации.
func contains(hay string, m Mention) bool {
	h, n := norm(hay), norm(m.Text)
	if n == "" {
		return true
	}
	if m.Kind != "number" {
		return strings.Contains(h, n)
	}
	for i := 0; ; {
		j := strings.Index(h[i:], n)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(n)
		before := start == 0 || !isDigit(h[start-1])
		after := end == len(h) || !isDigit(h[end])
		if before && after {
			return true
		}
		i = start + 1
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// StateProblems — что не так с фактами о состоянии в ответе. Пусто —
// принято. Каждая проблема — словами: что не так и что сделать.
//
// Правила:
//  1. Каждый state_fact ссылается на call_id вызова этого хода, причём
//     инструмента состояния (не docs_search), и без ошибки; упоминания в
//     самом факте (ветка, статус, путь, число) — подстроки результата
//     именно этого вызова.
//  2. Каждое упоминание в answer должно быть в вопросе, в цитатах ответа
//     или в результате одного из вызовов, названных в state_facts.
func StateProblems(a Answer, calls *CallLog, question string) []string {
	var problems []string
	var backed []string // результаты вызовов, на которые ссылаются state_facts
	for i, f := range a.StateFacts {
		id := strings.TrimSpace(f.CallID)
		c, ok := calls.Get(id)
		switch {
		case id == "":
			problems = append(problems, fmt.Sprintf("state_facts[%d] «%s» без call_id: укажи id вызова инструмента состояния этого хода, из результата которого взят факт", i, short(f.Fact)))
			continue
		case !ok:
			problems = append(problems, fmt.Sprintf("state_facts[%d]: вызова с call_id %q в этом ходе не было; возьми id из своего вызова инструмента (%s)", i, id, stateHint(calls)))
			continue
		case c.Final || !IsStateTool(c.Tool):
			problems = append(problems, fmt.Sprintf("state_facts[%d]: %s — не инструмент состояния; факт о ветке, статусах, файлах или прогонах берётся из git_*, assignments_*, assignment_get, demo_report, repo_*, read_file", i, c.Tool))
			continue
		case c.Error:
			problems = append(problems, fmt.Sprintf("state_facts[%d]: вызов %s (%s) закончился ошибкой — на него опереться нельзя; повтори вызов с верными аргументами или убери факт", i, c.Tool, id))
			continue
		}
		backed = append(backed, c.Result)
		for _, m := range Mentions(f.Fact) {
			if !contains(c.Result, m) && !contains(question, m) {
				problems = append(problems, fmt.Sprintf("state_facts[%d]: %s «%s» нет в результате вызова %s (%s) — перенеси значение из результата дословно или сошлись на другой вызов", i, kindName(m.Kind), m.Text, c.Tool, id))
			}
		}
	}
	// Опора для упоминаний ответа: вопрос, цитаты, результаты названных вызовов.
	hay := question + "\n" + strings.Join(backed, "\n")
	for _, q := range a.Quotes {
		hay += "\n" + q.Text
	}
	for _, m := range Mentions(a.Answer) {
		if contains(hay, m) {
			continue
		}
		problems = append(problems, fmt.Sprintf("в ответе %s «%s», но этого нет ни в цитатах, ни в результатах инструментов из state_facts: %s и добавь state_facts[{fact, call_id}] с id этого вызова — либо убери упоминание",
			kindName(m.Kind), m.Text, adviseTool(m.Kind, calls)))
	}
	return problems
}

func kindName(kind string) string {
	switch kind {
	case "branch":
		return "ветка"
	case "status":
		return "статус"
	case "path":
		return "путь"
	}
	return "число"
}

// adviseTool — какой инструмент даёт опору упоминанию такого рода.
func adviseTool(kind string, calls *CallLog) string {
	if sc := calls.StateCalls(); len(sc) > 0 {
		names := make([]string, 0, len(sc))
		for _, c := range sc {
			names = append(names, c.Tool+" ("+c.CallID+")")
		}
		return "возьми значение из уже сделанного вызова " + strings.Join(names, ", ")
	}
	switch kind {
	case "branch":
		return "вызови git_branch"
	case "status":
		return "вызови assignments_list или assignment_get"
	case "path":
		return "вызови repo_tree, assignments_list или demo_report"
	}
	return "вызови инструмент состояния (assignments_list, demo_report, git_log)"
}

func stateHint(calls *CallLog) string {
	sc := calls.StateCalls()
	if len(sc) == 0 {
		return "в этом ходе инструменты состояния ещё не вызывались — сначала вызови нужный"
	}
	ids := make([]string, 0, len(sc))
	for _, c := range sc {
		ids = append(ids, c.CallID+" — "+c.Tool)
	}
	return "были вызовы: " + strings.Join(ids, ", ")
}

func short(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return string(r)
}
