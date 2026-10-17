package cite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/kb"
)

// citeHits — выдача для проверок: два фрагмента с настоящим текстом вида
// «как в корпусе» (кавычки, ё, тире, неразрывный пробел).
func citeHits() []kb.Hit {
	return []kb.Hit{
		{Chunk: kb.Chunk{ID: "red-panda/structure/013", DocID: "red-panda", Title: "Малая панда", Path: []string{"Образ жизни", "Питание"},
			Text: "Малые панды питаются в основном бамбуком. На приёмы пищи они тратят по 13 часов в день — это «почти всё» время бодрствования."}},
		{Chunk: kb.Chunk{ID: "kharza/structure/002", DocID: "kharza", Title: "Харза", Section: "Вступление",
			Text: "Масса самцов харзы — 2,5–5,8 кг, самок — 1,1–3,8 кг. Распространена на Дальнем Востоке."}},
	}
}

func cited(answer string, sources []string, quotes ...[2]string) Cited {
	c := Cited{Status: StatusAnswered, Answer: answer}
	for _, s := range sources {
		c.Sources = append(c.Sources, CitedSource{ChunkID: s})
	}
	for _, q := range quotes {
		c.Quotes = append(c.Quotes, CitedQuote{ChunkID: q[0], Text: q[1]})
	}
	return c
}

func TestCheck(t *testing.T) {
	hits := citeHits()
	rp, kh := hits[0].ID, hits[1].ID

	// Дословно после нормализации: регистр, ё/е, кавычки, тире, пробелы.
	c := cited("Малая панда ест по 13 часов в день.", []string{rp},
		[2]string{rp, "на приемы пищи они тратят по 13 часов в день - это \"почти   всё\""})
	if ck := Check(c, hits, true); !ck.OK || ck.Verbatim != 1 || !ck.HasSources || !ck.HasQuotes || len(ck.Problems) != 0 {
		t.Fatalf("дословная цитата: %+v", ck)
	}
	// Пропуск «…» — каждая часть ищется отдельно; числа — из цитат.
	c = cited("Самцы харзы весят 2,5–5,8 кг.", []string{kh}, [2]string{kh, "Масса самцов харзы … 2,5–5,8 кг"})
	if ck := Check(c, hits, true); !ck.OK {
		t.Fatalf("многоточие: %+v", ck)
	}
	c.Quotes[0].Text = "Масса самцов харзы ... 2,5–5,8 кг"
	if ck := Check(c, hits, true); !ck.OK {
		t.Fatalf("три точки: %+v", ck)
	}

	// Пересказ, короткая цитата, чужой chunk_id.
	c = cited("Малая панда ест 13 часов.", []string{rp, "manul/structure/004"},
		[2]string{rp, "панды едят по 13 часов в сутки"}, [2]string{rp, "13 часов"}, [2]string{"manul/structure/004", "манул ест пищух и полёвок"})
	ck := Check(c, hits, true)
	if ck.OK || strings.Join(ck.UnknownIDs, ",") != "manul/structure/004" || fmt.Sprint(ck.NotVerbatim) != "[0 1 2]" || ck.Verbatim != 0 {
		t.Fatalf("плохие цитаты: %+v", ck)
	}
	all := strings.Join(ck.Problems, "\n")
	for _, want := range []string{"цитата 1 (red-panda/structure/013) не найдена во фрагменте дословно", "цитата 2 (red-panda/structure/013) короче 15 символов",
		"цитата 3 ссылается на manul/structure/004", "не выдавались в этом ходе; источники и цитаты — только из выдачи: red-panda/structure/013, kharza/structure/002"} {
		if !strings.Contains(all, want) {
			t.Errorf("нет проблемы %q в\n%s", want, all)
		}
	}

	// Нет источников и цитат, пустой ответ.
	ck = Check(Cited{Status: "Answered"}, hits, true)
	if ck.OK || ck.HasSources || ck.HasQuotes || len(ck.Problems) != 3 {
		t.Fatalf("пусто: %+v", ck)
	}

	// Числа без цитаты: строго — проблема, мягко — только метрика.
	c = cited("Малая панда ест по 13 часов в день и спит 9 часов; [1] это 2 трети суток.", []string{rp},
		[2]string{rp, "На приёмы пищи они тратят по 13 часов в день"})
	ck = Check(c, hits, true)
	if ck.OK || strings.Join(ck.NumbersMissing, ",") != "9,2" || !strings.Contains(strings.Join(ck.Problems, ";"), "чисел ответа 9, 2 нет ни в одной цитате") {
		t.Fatalf("числа строго: %+v", ck)
	}
	if ck = Check(c, hits, false); !ck.OK || len(ck.NumbersMissing) != 2 {
		t.Fatalf("числа мягко: %+v", ck)
	}
	// «5 тысяч» в ответе = «5 000» в цитате (разбор Rule).
	h := kb.Hit{Chunk: kb.Chunk{ID: "x/structure/001", Text: "Тираж монеты — 5 000 экземпляров, выпущена 9 августа 2024 года."}}
	c = cited("Тираж — 5 тысяч, выпуск 9 августа 2024 года.", []string{h.ID}, [2]string{h.ID, "Тираж монеты — 5 000 экземпляров, выпущена 9 августа 2024 года"})
	if ck = Check(c, []kb.Hit{h}, true); !ck.OK {
		t.Fatalf("тысячи: %+v", ck)
	}

	// unknown: нужен только уточняющий вопрос.
	if ck = Check(Cited{Status: StatusUnknown, Answer: "в базе нет"}, nil, true); ck.OK || !strings.Contains(ck.Problems[0], "clarify") {
		t.Fatalf("unknown без уточнения: %+v", ck)
	}
	if ck = Check(Cited{Status: " unknown ", Clarify: "Рассказать, чем харза питается?"}, nil, true); !ck.OK {
		t.Fatalf("unknown: %+v", ck)
	}
	if ck = Check(Cited{Status: "maybe"}, hits, true); ck.OK || !strings.Contains(ck.Problems[0], "неизвестный status") {
		t.Fatalf("статус: %+v", ck)
	}
}

// TestGate — «не знаю» кодом: пустая выдача; лучший косинус ниже порога
// индекса; BM25 и индекс без порога — решает модель.
func TestGate(t *testing.T) {
	hits := citeHits()
	hits[0].Score, hits[1].Score = 0.812, 0.790
	dense := kb.SearchInfo{Mode: kb.Dense}
	if g, why := Gate(nil, dense, 0.829); !g || !strings.Contains(why, "ничего не найдено") {
		t.Fatalf("пустая выдача: %v %q", g, why)
	}
	if g, why := Gate(hits, dense, 0.829); !g || why != "лучший косинус 0.812 ниже порога 0.829" {
		t.Fatalf("ниже порога: %v %q", g, why)
	}
	if g, why := Gate(hits, dense, 0.8); g || why != "" {
		t.Fatalf("выше порога: %v %q", g, why)
	}
	if g, _ := Gate(hits, dense, 0); g {
		t.Fatal("индекс без порога")
	}
	if g, _ := Gate(hits, kb.SearchInfo{Mode: kb.BM25, Fallback: "эмбеддер не задан"}, 0.829); g {
		t.Fatal("BM25 — своя шкала, порог не применяется")
	}
}

func TestFinishSchema(t *testing.T) {
	for _, only := range []bool{false, true} {
		var v struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(FinishSchema(only), &v); err != nil {
			t.Fatalf("%v: %v", only, err)
		}
		enum := strings.Join(v.Properties["status"].Enum, ",")
		req := strings.Join(v.Required, ",")
		switch {
		case only && (enum != "unknown" || !strings.Contains(req, "clarify")):
			t.Fatalf("только unknown: %s / %s", enum, req)
		case !only && (enum != "answered,unknown" || strings.Contains(req, "clarify")):
			t.Fatalf("оба: %s / %s", enum, req)
		}
	}
}

func args(c Cited) json.RawMessage {
	b, _ := json.Marshal(c)
	return b
}

func TestFinisher(t *testing.T) {
	ctx := context.Background()
	hits := citeHits()
	rp := hits[0].ID
	f := Finisher(func() []kb.Hit { return hits }, false, "")
	if f.Name != FinishName || !json.Valid(f.Parameters) || strings.Contains(f.Description, "только status unknown") {
		t.Fatalf("описание: %+v", f)
	}
	good := cited("Малая панда ест по 13 часов в день.", []string{rp}, [2]string{rp, "На приёмы пищи они тратят по 13 часов в день"})
	bad := cited("Малая панда ест по 13 часов в день.", []string{rp}, [2]string{rp, "панды едят по 13 часов в сутки"})

	// Отказ, затем исправление: Rejects = 1.
	if _, err := f.Handle(ctx, "c1", args(bad)); err == nil || !strings.Contains(err.Error(), "ответ не принят: цитата 1") {
		t.Fatalf("отказ: %v", err)
	}
	res, err := f.Handle(ctx, "c2", args(good))
	r, _ := res.(*CitedResult)
	if err != nil || r == nil || !r.Check.OK || r.Check.Rejects != 1 || r.Check.Unverified || len(r.Hits) != 2 {
		t.Fatalf("исправление: %+v %v", res, err)
	}

	// После MaxRejects отказов — приём с Unverified и пометкой в тексте.
	f = Finisher(func() []kb.Hit { return hits }, false, "")
	for i := 0; i < MaxRejects; i++ {
		if _, err := f.Handle(ctx, "c", args(bad)); err == nil {
			t.Fatalf("отказ %d", i+1)
		}
	}
	res, err = f.Handle(ctx, "c", args(bad))
	r, _ = res.(*CitedResult)
	if err != nil || r == nil || !r.Check.Unverified || r.Check.OK || r.Check.Rejects != MaxRejects ||
		!strings.Contains(r.Text(), "_Не проверено: цитата 1") {
		t.Fatalf("не проверено: %+v %v", r, err)
	}

	// Числа без цитаты: отказ один раз, второй вызов тот же — принят.
	f = Finisher(func() []kb.Hit { return hits }, false, "")
	nums := cited("Малая панда ест по 13 часов, это больше половины из 24 часов.", []string{rp}, [2]string{rp, "На приёмы пищи они тратят по 13 часов в день"})
	if _, err := f.Handle(ctx, "c", args(nums)); err == nil || !strings.Contains(err.Error(), "чисел ответа 24") {
		t.Fatalf("числа: %v", err)
	}
	res, err = f.Handle(ctx, "c", args(nums))
	if r, _ = res.(*CitedResult); err != nil || !r.Check.OK || strings.Join(r.Check.NumbersMissing, ",") != "24" || r.Check.Rejects != 0 {
		t.Fatalf("числа второй раз (отказ только из-за чисел — не в счёт): %+v %v", res, err)
	}

	// Только unknown (Gate): answered — отказ «контекст слабый…», unknown —
	// принят с Forced; после MaxRejects answered заменяется на «не знаю».
	f = Finisher(func() []kb.Hit { return nil }, true, "фильтр релевантности отсёк все фрагменты")
	if !strings.Contains(f.Description, "только status unknown: фильтр релевантности отсёк все фрагменты") {
		t.Fatalf("описание gate: %q", f.Description)
	}
	if _, err := f.Handle(ctx, "c", args(good)); err == nil || !strings.Contains(err.Error(), onlyUnknownProblem) {
		t.Fatalf("gate: %v", err)
	}
	res, err = f.Handle(ctx, "c", args(Cited{Status: StatusUnknown, Answer: "В базе знаний нет данных о жирафах", Clarify: "Спросить о хищных?"}))
	if r, _ = res.(*CitedResult); err != nil || !r.Check.Forced || !r.Check.OK || r.Check.Rejects != 1 {
		t.Fatalf("gate unknown: %+v %v", res, err)
	}
	if r.Text() != "Не знаю: в базе знаний нет данных о жирафах\n\nУточните: Спросить о хищных?" {
		t.Fatalf("текст unknown: %q", r.Text())
	}
	f = Finisher(func() []kb.Hit { return nil }, true, "x")
	for i := 0; i < MaxRejects; i++ {
		f.Handle(ctx, "c", args(good))
	}
	res, err = f.Handle(ctx, "c", args(good))
	if r, _ = res.(*CitedResult); err != nil || !r.Cited.Unknown() || !r.Check.Forced || !r.Check.Unverified || r.Cited.Clarify != defaultClarify ||
		strings.Contains(r.Text(), "13 часов") {
		t.Fatalf("gate после отказов: %+v %v", r, err)
	}
	// Пустая выдача без Gate — тоже «только unknown».
	f = Finisher(func() []kb.Hit { return nil }, false, "")
	if _, err := f.Handle(ctx, "c", args(good)); err == nil || !strings.Contains(err.Error(), "в выдаче нет фрагментов") {
		t.Fatalf("пустая выдача: %v", err)
	}
	// Битые аргументы — отказ; после MaxRejects — «не знаю» с пометкой
	// «не проверено», а не бесконечный цикл.
	if _, err := f.Handle(ctx, "c", json.RawMessage(`{"status":`)); err == nil || !strings.Contains(err.Error(), "не разобраны") {
		t.Fatalf("битые: %v", err)
	}
	f = Finisher(func() []kb.Hit { return hits }, false, "")
	for i := 0; i < MaxRejects; i++ {
		if _, err := f.Handle(ctx, "c", json.RawMessage(`{"status":`)); err == nil {
			t.Fatalf("битые, отказ %d", i+1)
		}
	}
	res, err = f.Handle(ctx, "c", json.RawMessage(`{"status":`))
	if r, _ = res.(*CitedResult); err != nil || !r.Cited.Unknown() || !r.Check.Unverified || r.Check.Rejects != MaxRejects ||
		!strings.HasPrefix(r.Text(), "Не знаю: "+unverifiedAnswer) || len(r.Cited.Sources) != 2 || !strings.Contains(r.Check.Problems[0], "не разобраны") {
		t.Fatalf("битые после отказов: %+v %v", r, err)
	}
}

// TestFinisherGateAndNearest — «только unknown» в момент вызова (Gate) и
// «ближайшее в базе» у «не знаю».
func TestFinisherGateAndNearest(t *testing.T) {
	ctx := context.Background()
	hits := citeHits()
	rp := hits[0].ID
	good := cited("Малая панда ест по 13 часов в день.", []string{rp}, [2]string{rp, "На приёмы пищи они тратят по 13 часов в день"})
	gated := true
	f := FinisherOf(FinishOptions{Hits: func() []kb.Hit { return hits }, Gate: func() (bool, string, []kb.Hit) {
		if gated {
			return true, "фильтр отсёк всё", nil
		}
		return false, "", hits[:1]
	}})
	// Схема в чате — без ограничения: решение не известно до хода.
	if strings.Contains(string(f.Parameters), `"enum":["unknown"]`) {
		t.Fatal("схема с ограничением")
	}
	if _, err := f.Handle(ctx, "c", args(good)); err == nil || !strings.Contains(err.Error(), onlyUnknownProblem+" (фильтр отсёк всё)") {
		t.Fatalf("gate: %v", err)
	}
	// Другой поиск хода нашёл фрагменты — ответ принят.
	gated = false
	res, err := f.Handle(ctx, "c", args(good))
	r, _ := res.(*CitedResult)
	if err != nil || !r.Check.OK || r.Check.Gated || r.Check.Relevant != 1 || r.Check.Rejects != 1 {
		t.Fatalf("после второго поиска: %+v %v", r, err)
	}

	// «Не знаю» с выдачей — ближайшее найденное: источники модели из
	// релевантной выдачи, иначе — первые фрагменты выдачи.
	f = Finisher(func() []kb.Hit { return hits }, false, "")
	res, err = f.Handle(ctx, "c", args(Cited{Status: StatusUnknown, Answer: "в базе знаний нет данных о сне малой панды", Clarify: "Рассказать о питании?",
		Sources: []CitedSource{{ChunkID: hits[1].ID}, {ChunkID: "x/structure/009"}}}))
	r, _ = res.(*CitedResult)
	if err != nil || !r.Check.OK || r.Check.Forced || !r.Check.HasSources || r.Check.Relevant != 2 ||
		fmt.Sprint(r.Cited.Sources) != "[{"+hits[1].ID+"}]" {
		t.Fatalf("unknown с источником модели: %+v %v", r, err)
	}
	want := "Не знаю: в базе знаний нет данных о сне малой панды\n\nУточните: Рассказать о питании?\n\n**Ближайшее в базе:**\n[1] Харза › Вступление (`kharza/structure/002`)"
	if r.Text() != want {
		t.Fatalf("текст:\n%s\nждали\n%s", r.Text(), want)
	}
	f = Finisher(func() []kb.Hit { return hits }, false, "")
	res, _ = f.Handle(ctx, "c", args(Cited{Status: StatusUnknown, Clarify: "О чём именно?"}))
	if r, _ = res.(*CitedResult); len(r.Cited.Sources) != 2 || r.Cited.Sources[0].ChunkID != rp {
		t.Fatalf("unknown без источников модели: %+v", r.Cited.Sources)
	}
	// Пустая выдача (Gate) — «не знаю» без источников.
	f = Finisher(func() []kb.Hit { return nil }, true, "ниже порога")
	res, _ = f.Handle(ctx, "c", args(Cited{Status: StatusUnknown, Clarify: "О чём именно?", Sources: []CitedSource{{ChunkID: rp}}}))
	if r, _ = res.(*CitedResult); len(r.Cited.Sources) != 0 || r.Check.Relevant != 0 || !r.Check.Forced || strings.Contains(r.Text(), "Ближайшее") {
		t.Fatalf("unknown без выдачи: %+v", r)
	}
	// Оборванный ход — «не знаю» с пометкой и ближайшим найденным.
	u := UnverifiedResult(agent.ErrProtocol, hits, hits)
	if !u.Check.Unverified || !u.Cited.Unknown() || len(u.Cited.Sources) != 2 || !strings.Contains(u.Text(), "**Ближайшее в базе:**") ||
		!strings.Contains(u.Check.Problems[0], "текстом") {
		t.Fatalf("оборванный ход: %+v", u)
	}
}

// TestQuoteProblem — пропуск «…» по порядку частей, короткий и без
// отрицаний; пробелы вокруг тире.
func TestQuoteProblem(t *testing.T) {
	kharza := "Типичная длина тела (не считая хвоста) самцов равняется 50—72 см при массе в 2,5—5,8 кг, самок — до 62 см при массе 1,1—3,8 кг. " +
		"По другим данным, длина тела — 75-80 см. Как правило, длина хвоста взрослых особей составляет 35—40 см (около ⅔ всей длины тела). " +
		"Харза может дорастать до 75—80 см (не учитывая хвост), а длина её хвоста иногда достигает 45 см. " +
		"Харза не опасна для человека и избегает встреч с ним."
	cases := []struct {
		quote, want string
	}{
		{"самцов равняется 50—72 см … при массе в 2,5—5,8 кг", ""},
		{"самцов … при массе в 2,5 – 5,8 кг", ""},                   // пробелы вокруг тире
		{"при массе в 2,5 - 5,8 кг, самок — до 62 см.", ""},         // концевая точка
		{"самцов равняется 50—72 см [...] самок — до 62 см", ""},    // «[…]»
		{"самок … при массе в 2,5—5,8 кг", gapProblem},              // части не по порядку
		{"Харза … опасна для человека", gapProblem},                 // пропущено «не»
		{"Типичная длина тела … избегает встреч с ним", gapProblem}, // пропуск длиннее 200
		{"при массе в 3—5 кг, самок", "не найдена во фрагменте дословно"},
	}
	for _, c := range cases {
		got := quoteProblem(c.quote, kharza)
		if (c.want == "" && got != "") || (c.want != "" && !strings.Contains(got, c.want)) {
			t.Errorf("%q: %q, ждали %q", c.quote, got, c.want)
		}
	}
	// В Check: отказ с подсказкой без «…».
	h := kb.Hit{Chunk: kb.Chunk{ID: "kharza/structure/002", DocID: "kharza", Text: kharza}}
	ck := Check(cited("Харза опасна для человека.", []string{h.ID}, [2]string{h.ID, "Харза … опасна для человека"}), []kb.Hit{h}, true)
	if ck.OK || !strings.Contains(strings.Join(ck.Problems, ";"), "пропуск в цитате меняет смысл: процитируй без «…»") {
		t.Fatalf("Check: %+v", ck)
	}
}

// TestCheckNumbers — числа вопроса и версии не требуют цитаты.
func TestCheckNumbers(t *testing.T) {
	h := kb.Hit{Chunk: kb.Chunk{ID: "manul/structure/022", DocID: "manul", Title: "Манул", Text: "Численность манула в 2020 году оценивалась в 15 тысяч особей."}}
	c := cited("По MDD v2.5 в 2020 году манулов было около 15 тысяч.", []string{h.ID}, [2]string{h.ID, "оценивалась в 15 тысяч особей"})
	ck := CheckWith(c, []kb.Hit{h}, CheckOptions{StrictNumbers: true, Question: "Сколько манулов было в 2020 году?"})
	if !ck.OK || len(ck.NumbersMissing) != 0 {
		t.Fatalf("числа вопроса и версия: %+v", ck)
	}
	if ck = Check(c, []kb.Hit{h}, true); ck.OK || strings.Join(ck.NumbersMissing, ",") != "2020" {
		t.Fatalf("без вопроса 2020 — без цитаты: %+v", ck)
	}
}

func TestCitedText(t *testing.T) {
	hits := citeHits()
	c := cited("Малая панда ест по 13 часов в день.", []string{hits[0].ID},
		[2]string{hits[0].ID, "На приёмы пищи они тратят\nпо 13 часов в день"}, [2]string{hits[1].ID, "Масса самцов харзы — 2,5–5,8 кг"})
	r := &CitedResult{Cited: c, Hits: hits}
	want := "Малая панда ест по 13 часов в день.\n\n**Источники:**\n" +
		"[1] Малая панда › Образ жизни › Питание (`red-panda/structure/013`)\n" +
		"[2] Харза › Вступление (`kharza/structure/002`)\n\n" +
		"**Цитаты:**\n> «На приёмы пищи они тратят по 13 часов в день» [1]\n> «Масса самцов харзы — 2,5–5,8 кг» [2]"
	if got := r.Text(); got != want {
		t.Fatalf("текст:\n%s\nждали\n%s", got, want)
	}
	// Без выдачи — только chunk_id.
	if got := c.Text(); !strings.Contains(got, "[1] `red-panda/structure/013`") {
		t.Fatalf("без выдачи: %s", got)
	}
	if c.EvalText() != c.Answer {
		t.Fatal("оценка — по ответу без обвязки")
	}
	u := Cited{Status: StatusUnknown, Clarify: "О каком животном речь?"}
	if u.Text() != "Не знаю: в базе знаний нет ответа на этот вопрос.\n\nУточните: О каком животном речь?" ||
		u.EvalText() != "Не знаю: в базе знаний нет ответа на этот вопрос." {
		t.Fatalf("unknown: %q / %q", u.Text(), u.EvalText())
	}
	if got := (Cited{Status: StatusUnknown, Answer: "Не знаю, данных нет."}).EvalText(); got != "Не знаю, данных нет." {
		t.Fatalf("без повтора «не знаю»: %q", got)
	}
}

// TestFinisherNumbers — числа без цитаты: подсказка один раз и не в счёт
// отказов, затем приём с NumbersMissing и Grounded = false; полная опора —
// Grounded.
func TestFinisherNumbers(t *testing.T) {
	ctx := context.Background()
	sable := kb.Hit{Chunk: kb.Chunk{ID: "sable/structure/003", DocID: "sable", Title: "Соболь", Text: "Масса самцов соболя — от 880 до 1800 г."}}
	hits := func() []kb.Hit { return []kb.Hit{sable} }
	own := cited("Соболь весит до 2 кг.", []string{sable.ID}, [2]string{sable.ID, "Масса самцов соболя — от 880 до 1800 г."})
	f := FinisherOf(FinishOptions{Hits: hits})
	if _, err := f.Handle(ctx, "c", args(own)); err == nil || !strings.Contains(err.Error(), "чисел ответа 2 нет") {
		t.Fatalf("мягкий отказ: %v", err)
	}
	res, err := f.Handle(ctx, "c", args(own))
	r, _ := res.(*CitedResult)
	if err != nil || r == nil || !r.Check.OK || r.Check.Unverified || r.Check.Grounded || r.Check.Rejects != 0 ||
		strings.Join(r.Check.NumbersMissing, ",") != "2" {
		t.Fatalf("мягкий приём: %+v %v", res, err)
	}
	ok := cited("Соболь весит от 880 до 1800 г.", []string{sable.ID}, [2]string{sable.ID, "Масса самцов соболя — от 880 до 1800 г."})
	res, err = FinisherOf(FinishOptions{Hits: hits}).Handle(ctx, "c", args(ok))
	if r, _ = res.(*CitedResult); err != nil || r == nil || !r.Check.OK || !r.Check.Grounded {
		t.Fatalf("с опорой: %+v %v", res, err)
	}
}
