package kb

// Контрольные вопросы базы знаний — eval/questions.json. Разметка не зависит
// от стратегии чанкинга: релевантность задаёт дословный фрагмент-
// доказательство (Evidence.Quote) из документа, а не chunk_id. Чанк
// релевантен вопросу, если покрывает не меньше EvidenceCover фрагмента
// (по смещениям в Doc.Text) хотя бы одного доказательства.

// QuestionsSchema — версия формата файла вопросов.
const QuestionsSchema = 1

// EvidenceCover — какую долю фрагмента должен покрыть чанк.
const EvidenceCover = 0.8

// Наборы.
const (
	SplitTest = "test" // 10 контрольных вопросов заданий; формулировки не меняются
	SplitDev  = "dev"  // подбор порогов и параметров
	SplitOut  = "out"  // вне корпуса: ответа в базе нет (калибровка «не знаю»)
)

// Number — число, которое должно быть в ответе, с допуском.
type Number struct {
	V   float64 `json:"v"`
	Tol float64 `json:"tol,omitempty"`
}

// Expect — что должно быть в ответе (проверяется с v22). Must — группы
// синонимов: в ответе должна быть хотя бы одна форма из каждой группы.
type Expect struct {
	Must    [][]string `json:"must,omitempty"`
	MustNot []string   `json:"must_not,omitempty"`
	Numbers []Number   `json:"numbers,omitempty"`
	// Note — ожидание словами, для людей.
	Note string `json:"note,omitempty"`
}

// SourceRef — какой источник должен использоваться (для людей и отчёта).
type SourceRef struct {
	DocID   string `json:"doc_id"`
	Section string `json:"section,omitempty"`
}

// Evidence — дословный фрагмент документа, на котором держится ответ.
type Evidence struct {
	DocID string `json:"doc_id"`
	Quote string `json:"quote"`
}

// Question — контрольный вопрос.
type Question struct {
	ID    string `json:"id"`    // "T01", "D07", "O03"
	Split string `json:"split"` // test | dev | out
	// Type — fact | number | conflict | section | compare | multihop |
	// synonym | followup | aspect-missing | out-of-base.
	Type        string   `json:"type"`
	Q           string   `json:"q"`
	Paraphrases []string `json:"paraphrases,omitempty"`
	// Context — предыдущие реплики пользователя (по порядку) для вопроса-
	// продолжения (type followup): Q без них не понять — «а сколько он
	// весит?». Поиск и rewrite получают Context вместе с Q.
	Context    []string    `json:"context,omitempty"`
	Answerable bool        `json:"answerable"`
	Expect     *Expect     `json:"expect,omitempty"`
	Sources    []SourceRef `json:"sources,omitempty"`
	Evidence   []Evidence  `json:"evidence,omitempty"`
	// Discriminative — модель без базы отвечает неверно (проба v22); nil —
	// ещё не проверялось.
	Discriminative *bool  `json:"discriminative,omitempty"`
	Note           string `json:"note,omitempty"`
}

// QuestionSet — файл вопросов.
type QuestionSet struct {
	Schema    int        `json:"schema"`
	Version   int        `json:"version"`
	Questions []Question `json:"questions"`
}

// LoadQuestions, Verify и Split — в questions.go.

// Covers — какую долю фрагмента [qs, qe) покрывает отрезок [cs, ce).
func Covers(cs, ce, qs, qe int) float64 {
	if qe <= qs {
		return 0
	}
	lo, hi := cs, ce
	if qs > lo {
		lo = qs
	}
	if qe < hi {
		hi = qe
	}
	if hi <= lo {
		return 0
	}
	return float64(hi-lo) / float64(qe-qs)
}
