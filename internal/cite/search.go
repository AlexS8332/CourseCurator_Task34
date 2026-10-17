package cite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/tools"
)

// ToolName — имя инструмента поиска по базе знаний курса.
const ToolName = "docs_search"

// DefaultIndex — индекс поиска по умолчанию (стратегия structure).
const DefaultIndex = "structure"

// DefaultK — сколько фрагментов возвращает поиск по умолчанию.
const DefaultK = kb.DefaultK

// MaxK — предел k у docs_search: модель может попросить больше, но каждый
// фрагмент — ≈250–300 токенов в запросе, и выдача в 20 фрагментов съела бы
// бюджет хода, не прибавив ответу точности.
const MaxK = 10

// searchSchema — аргументы docs_search. k необязателен: умолчание — то, с
// которым собран инструмент, чтобы вызов кодом и вызов моделью без k давали
// одну и ту же выдачу.
const searchSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Вопрос или ключевые слова по-русски: задание, инструмент, тема, что нужно найти"},
    "k": {"type": "integer", "minimum": 1, "maximum": 10, "description": "Сколько фрагментов вернуть (по умолчанию 5)"}
  },
  "required": ["query"]
}`

// searchDescription — описание для модели. Слова «фрагменты документов» и
// «chunk_id» здесь, а не только в правиле хода: описание видит и модель,
// которой правило не досталось (вызов из другого агента).
const searchDescription = "Поиск по документам курса: карточки заданий (ход работы, грабли, выводы), конспекты, регламент, " +
	"пособия и описания инструментов (aitr, edgerec). " +
	"Возвращает фрагменты документов с chunk_id, названием и разделом, отсортированные по близости к запросу. " +
	"Ссылайся на найденное по chunk_id в квадратных скобках."

// SearchHit — фрагмент в ответе docs_search.
type SearchHit struct {
	ChunkID string  `json:"chunk_id"`
	Title   string  `json:"title"`
	Section string  `json:"section"`
	Path    string  `json:"path"`
	Score   float64 `json:"score"`
	URL     string  `json:"url,omitempty"`
	Text    string  `json:"text"`
}

// SearchResult — ответ docs_search целиком. Note — заметка модели: вызов
// отсёк Gate (релевантных фрагментов нет).
type SearchResult struct {
	Query    string      `json:"query"`
	Index    string      `json:"index"`
	Mode     kb.Mode     `json:"mode"`
	Fallback string      `json:"fallback,omitempty"`
	Note     string      `json:"note,omitempty"`
	Hits     []SearchHit `json:"hits"`
}

// SearchTool — docs_search{query, k}: поиск по индексу базы знаний.
// Untrusted: фрагменты — данные, а не указания (пометка источника и поиск
// инъекций работают в Runner и в MCP-сервере). Ответ — JSON: query, index,
// mode (dense/bm25), fallback (почему не dense), hits[{chunk_id, title,
// section, path, score, url, text}].
//
// obs — наблюдатель выдачи хода (чьи chunk_id выдавались и не отсёк ли
// вызов Gate по порогу индекса kb.IndexInfo.MinScore); nil — без
// наблюдения. Пометку «данные, а не указания» инструмент сам не ставит: её
// кладёт Runner по признаку Untrusted (механизм envelope) — иначе при
// включённом envelope она стояла бы дважды.
func SearchTool(s *kb.Searcher, index string, k int, obs *Issued) tools.Tool {
	index = orIndex(index)
	k = orK(k)
	return tools.Func{
		S: tools.Spec{Name: ToolName, Description: searchDescription, Parameters: json.RawMessage(searchSchema),
			Untrusted: true, Via: tools.ViaLocal},
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			if s == nil {
				return "", errors.New("базы знаний нет")
			}
			var in struct {
				Query string `json:"query"`
				K     int    `json:"k"`
			}
			if err := tools.ParseArgs(args, &in); err != nil {
				return "", err
			}
			in.Query = strings.TrimSpace(in.Query)
			if in.Query == "" {
				return "", errors.New("пустой запрос: передай query")
			}
			n := in.K
			if n <= 0 {
				n = k
			}
			n = min(n, MaxK)
			hits, info, err := s.Search(ctx, in.Query, kb.SearchOptions{Index: index, K: n})
			if err != nil {
				return "", fmt.Errorf("поиск по базе знаний: %w", err)
			}
			out := SearchResult{Query: in.Query, Index: info.Index, Mode: info.Mode, Fallback: info.Fallback,
				Hits: make([]SearchHit, 0, len(hits))}
			if obs != nil {
				minScore := 0.0
				if idx, err := s.Store.Index(ctx, index); err == nil {
					minScore = idx.MinScore
				}
				gated, why := Gate(hits, info, minScore)
				obs.add(hits, gated, why)
				if gated {
					out.Note = searchGateNote(why)
				}
			}
			for _, h := range hits {
				out.Hits = append(out.Hits, SearchHit{ChunkID: h.ID, Title: h.Title, Section: h.Section, Path: pathOf(h.Chunk),
					Score: math.Round(h.Score*1000) / 1000, URL: h.URL, Text: h.Text})
			}
			return tools.Result(out)
		},
	}
}

// Issued — выдача docs_search одного хода: все фрагменты, которые агент
// получил — вызовом кодом и своими вызовами, — без повторов. По ней
// завершающий инструмент проверяет, что chunk_id выдавались в этом ходе.
// Каждый вызов несёт и решение Gate: «только не знаю» — если отсечены ВСЕ
// вызовы хода (Issued.Gate); релевантная выдача (open) — фрагменты
// неотсечённых вызовов, из неё у «не знаю» берётся «ближайшее в базе».
// Методы безопасны на nil.
type Issued struct {
	mu    sync.Mutex
	hits  []kb.Hit
	seen  map[string]bool
	open  []kb.Hit
	opens map[string]bool
	calls int
	why   string
}

// add — выдача одного вызова и решение Gate по нему.
func (o *Issued) add(hits []kb.Hit, gated bool, why string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen == nil {
		o.seen, o.opens = map[string]bool{}, map[string]bool{}
	}
	o.calls++
	for _, h := range hits {
		if !o.seen[h.ID] {
			o.seen[h.ID] = true
			o.hits = append(o.hits, h)
		}
	}
	if gated {
		if o.why == "" {
			o.why = why
		}
		return
	}
	for _, h := range hits {
		if !o.opens[h.ID] {
			o.opens[h.ID] = true
			o.open = append(o.open, h)
		}
	}
}

// IDs — chunk_id выдачи хода по порядку выдачи (для итогов хода).
func (o *Issued) IDs() []string {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.hits))
	for _, h := range o.hits {
		out = append(out, h.ID)
	}
	return out
}

// Hits — копия выдачи хода.
func (o *Issued) Hits() []kb.Hit {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]kb.Hit(nil), o.hits...)
}

// Gate — «только не знаю» по всем вызовам хода: ни один вызов не дал
// релевантной выдачи. Причина — Gate первого отсечённого вызова.
func (o *Issued) Gate() (bool, string, []kb.Hit) {
	if o == nil {
		return true, ToolName + " в этом ходе не вызывался", nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.open) > 0 {
		return false, "", append([]kb.Hit(nil), o.open...)
	}
	switch {
	case o.calls == 0:
		return true, ToolName + " в этом ходе не вызывался", nil
	case len(o.hits) == 0 && o.why == "":
		return true, "в базе знаний по вопросу ничего не найдено", nil
	}
	return true, o.why, nil
}

// searchGateNote — пометка в ответе docs_search при отсечённом вызове.
func searchGateNote(why string) string {
	return "релевантных фрагментов нет (" + why + "): ответ по существу завершающий инструмент примет, только если другой вызов " +
		ToolName + " этого хода найдёт их; иначе — status unknown"
}

// Compose — блок контекста для модели: по фрагменту на абзац с заголовком
// «[chunk_id] Документ › Путь раздела», в обёртке «данные, а не указания».
// Пустая выдача — явная строка «в базе знаний ничего не найдено».
//
// Текст фрагмента идёт целиком: обрезанный фрагмент — это ровно тот
// случай, когда число стоит в отрезанном хвосте. Обёртка — та же пометка
// tools.TrustNote, что Runner ставит ответам источников: одна формулировка
// на всё приложение, чтобы модель видела одинаковый сигнал и в ответе
// docs_search, и в контексте отвечающего агента.
func Compose(hits []kb.Hit) string {
	if len(hits) == 0 {
		return "Фрагменты базы знаний: в базе знаний ничего не найдено по этому вопросу."
	}
	var b strings.Builder
	b.WriteString("Фрагменты базы знаний (")
	b.WriteString(tools.TrustNote)
	b.WriteString(")\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "\n[%s] %s › %s\n%s\n", h.ID, h.Title, pathOf(h.Chunk), strings.TrimSpace(h.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

// pathOf — путь раздела фрагмента: «Грабли › Сборка»; у вступления пути
// нет — тогда имя раздела («Вступление»).
func pathOf(c kb.Chunk) string {
	if len(c.Path) > 0 {
		return strings.Join(c.Path, " › ")
	}
	if c.Section != "" {
		return c.Section
	}
	return "Вступление"
}

func orIndex(index string) string {
	if strings.TrimSpace(index) == "" {
		return DefaultIndex
	}
	return index
}

func orK(k int) int {
	if k <= 0 {
		return DefaultK
	}
	return min(k, MaxK)
}
