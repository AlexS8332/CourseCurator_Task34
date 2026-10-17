package review

import (
	"fmt"
	"sort"
	"strings"

	"aitr/34-files-agent/internal/kb"
)

// Текст ревью для человека (комментарий PR, консоль, окно) и
// inline-комментарии GitHub. Маркер дедупа (Marker) в текст не входит —
// его добавляет тот, кто постит.

// kindLabel — вид находки словами.
func kindLabel(kind string) string {
	switch kind {
	case KindBug:
		return "баг"
	case KindRule:
		return "правило"
	case KindDocs:
		return "документация"
	case KindRecommendation:
		return "рекомендация"
	}
	return orText(kind, "находка")
}

// severityLabel — тяжесть словами.
func severityLabel(s string) string {
	switch s {
	case SeverityMajor:
		return "major"
	case SeverityMinor:
		return "minor"
	}
	return orText(s, "—")
}

// SourceLabel — источник цитаты словами: «CLAUDE.md › Инструменты» (путь
// документа и путь раздела фрагмента из выдачи hits); фрагмента в выдаче
// нет — chunk_id как есть.
func SourceLabel(id string, hits []kb.Hit) string {
	id = strings.TrimSpace(id)
	for _, h := range hits {
		if h.ID != id {
			continue
		}
		doc := orText(h.DocID, h.Title)
		section := ""
		switch {
		case len(h.Path) > 0:
			section = strings.Join(h.Path, " › ")
		case h.Section != "":
			section = h.Section
		}
		if section == "" {
			return doc
		}
		return doc + " › " + section
	}
	return id
}

// Verdict — вердикт словами для заголовка.
func (t *Turn) Verdict() string {
	switch {
	case t == nil:
		return "ревью не получено"
	case t.Review.Verdict == VerdictOK:
		return "замечаний нет"
	case t.Review.Verdict == VerdictChanges:
		major, minor := t.count()
		return fmt.Sprintf("нужны правки — находок %d (major %d, minor %d)", len(t.Review.Findings), major, minor)
	}
	return "ревью не получено"
}

// count — сколько находок major и minor.
func (t *Turn) count() (major, minor int) {
	for _, f := range t.Review.Findings {
		if f.Severity == SeverityMajor {
			major++
		} else {
			minor++
		}
	}
	return major, minor
}

// findingLine — одна находка в markdown: строка, вид, тяжесть, текст и
// цитата правила со ссылкой на документ (у rule|docs) или опора bug.
func findingLine(f Finding, hits []kb.Hit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- **строка %d · %s · %s** — %s", f.Line, kindLabel(f.Kind), severityLabel(f.Severity), strings.TrimSpace(f.Text))
	switch f.Kind {
	case KindRule, KindDocs:
		if q := strings.Join(strings.Fields(f.Evidence.Quote), " "); q != "" {
			fmt.Fprintf(&b, "\n  > «%s» — %s", q, SourceLabel(f.Evidence.ChunkID, hits))
		} else if f.Evidence.ChunkID != "" {
			fmt.Fprintf(&b, "\n  > %s", SourceLabel(f.Evidence.ChunkID, hits))
		}
	case KindBug:
		if f.Evidence.Path != "" && f.Evidence.Line > 0 && (f.Evidence.Path != f.File || f.Evidence.Line != f.Line) {
			fmt.Fprintf(&b, " (см. %s:%d)", f.Evidence.Path, f.Evidence.Line)
		}
	}
	return b.String()
}

// Markdown — текст ревью: вердикт, находки по файлам (с цитатами правил
// и ссылкой «CLAUDE.md › раздел»), сводка, пометка «не проверено» и
// строка «запросов N · токенов M · $цена». Маркер дедупа — отдельно.
func (t *Turn) Markdown() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Ревью: " + t.Verdict() + "\n")
	byFile := map[string][]Finding{}
	var files []string
	for _, f := range t.Review.Findings {
		if _, ok := byFile[f.File]; !ok {
			files = append(files, f.File)
		}
		byFile[f.File] = append(byFile[f.File], f)
	}
	// Порядок файлов — как в diff, остальные — в конце по алфавиту.
	order := map[string]int{}
	for i, fd := range t.Input.Files {
		order[NormPath(fd.Path)] = i + 1
	}
	sort.SliceStable(files, func(i, j int) bool {
		oi, oj := order[files[i]], order[files[j]]
		if oi == 0 {
			oi = len(order) + 1
		}
		if oj == 0 {
			oj = len(order) + 1
		}
		if oi != oj {
			return oi < oj
		}
		return files[i] < files[j]
	})
	for _, file := range files {
		fs := byFile[file]
		sort.SliceStable(fs, func(i, j int) bool { return fs[i].Line < fs[j].Line })
		b.WriteString("\n### " + orText(file, "(файл не указан)") + "\n")
		for _, f := range fs {
			b.WriteString(findingLine(f, t.Hits) + "\n")
		}
	}
	if s := strings.TrimSpace(t.Review.Summary); s != "" {
		b.WriteString("\n**Сводка.** " + s + "\n")
	}
	if t.Unverified {
		b.WriteString("\n_Не проверено: " + orText(strings.Join(t.Check.Problems, "; "), "ревью принято после отказов проверки") + "._\n")
	}
	b.WriteString("\n" + t.StatsLine() + "\n")
	return b.String()
}

// StatsLine — «запросов N · токенов M · $цена» (неизвестный прайс — «?»).
func (t *Turn) StatsLine() string {
	st := t.Stats
	price := "?"
	if st.Cost.Known {
		price = fmt.Sprintf("$%.4f", st.Cost.USD)
	}
	return fmt.Sprintf("_запросов %d · токенов %d · %s_", st.Steps, st.Usage.Total, price)
}

// Comments — inline-комментарии для GitHub: по комментарию на находку,
// только к Commentable строкам diff (остальные — лишь в тексте ревью).
func (t *Turn) Comments() []Comment {
	if t == nil {
		return nil
	}
	var out []Comment
	for _, f := range t.Review.Findings {
		fd := FileByPath(t.Input.Files, f.File)
		if fd == nil || !fd.Commentable(f.Line) {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "**%s · %s** — %s", kindLabel(f.Kind), severityLabel(f.Severity), strings.TrimSpace(f.Text))
		switch f.Kind {
		case KindRule, KindDocs:
			if q := strings.Join(strings.Fields(f.Evidence.Quote), " "); q != "" {
				fmt.Fprintf(&b, "\n\n> «%s» — %s", q, SourceLabel(f.Evidence.ChunkID, t.Hits))
			}
		case KindBug:
			if f.Evidence.Path != "" && f.Evidence.Line > 0 && (f.Evidence.Path != f.File || f.Evidence.Line != f.Line) {
				fmt.Fprintf(&b, " (см. %s:%d)", f.Evidence.Path, f.Evidence.Line)
			}
		}
		out = append(out, Comment{Path: NormPath(fd.Path), Line: f.Line, Body: b.String()})
	}
	return out
}
