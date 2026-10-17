package docs

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
)

// Code — вид документа «код»: doc-комментарии пакетов и сигнатуры
// экспортов Go, функции scripts/aitr.ps1 с комментариями. Ревьюер PR
// (день 32) ищет по ним, что делает затронутый пакет и как называются
// его экспорты; сами файлы он читает инструментом read_file.
const Code Kind = "code"

// codeRoots — где лежат Go-пакеты: прямые подкаталоги этих каталогов
// (tools/edgerec, tools/curator/internal/gitx, tools/curator/cmd/curator).
// Вложенные глубже (internal/llm/llmtest) не берутся.
var codeRoots = []string{"tools", "tools/curator/internal", "tools/curator/cmd"}

// PSScript — скрипт оснастки, функции которого попадают в индекс кода.
const PSScript = "scripts/aitr.ps1"

// Имена групп разделов документа пакета.
const (
	codeTypes = "Типы"
	codeFuncs = "Функции"
)

// CodeDocs — документы вида code по корню курса: по одному на Go-пакет
// (ID — путь каталога с «/», Title «пакет X (путь)», Intro —
// doc-комментарий пакета, Sections — «Типы › Name» и «Функции › Name»
// с сигнатурой и первым абзацем комментария; методы — в разделе своего
// типа) и один на scripts/aitr.ps1 (Intro — комментарий шапки,
// Sections — по `function Name {` с комментарием над функцией или первым
// внутри, как в schema/aitr.md). Тесты (_test.go) не разбираются.
// Каталог без Go-файлов пропускается; синтаксическая ошибка в файле —
// ошибка с его путём.
func CodeDocs(root *course.Root) ([]corpus.Doc, error) {
	var docs []corpus.Doc
	seen := map[string]bool{}
	for _, r := range codeRoots {
		entries, err := os.ReadDir(root.Path(r))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || e.Name() == "testdata" || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			rel := r + "/" + e.Name()
			if seen[rel] {
				continue
			}
			seen[rel] = true
			d, ok, err := goPackageDoc(root.Path(rel), rel)
			if err != nil {
				return nil, err
			}
			if ok {
				docs = append(docs, d)
			}
		}
	}
	if raw, err := os.ReadFile(root.Path(PSScript)); err == nil {
		docs = append(docs, psDoc(string(raw), PSScript))
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	return docs, nil
}

// goPackageDoc — документ Go-пакета каталога dir; ok=false — Go-файлов
// нет (или только тесты).
func goPackageDoc(dir, rel string) (corpus.Doc, bool, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return corpus.Doc{}, false, fmt.Errorf("%s: %w", rel, err)
	}
	if len(pkgs) == 0 {
		return corpus.Doc{}, false, nil
	}
	// В каталоге обычно один пакет; если их несколько (редкость), берём
	// первый по имени — остальные без документа.
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	pkg := pkgs[names[0]]
	p := doc.New(pkg, rel, 0) // только экспорты

	d := corpus.Doc{
		Schema: corpus.Schema,
		ID:     rel,
		Source: string(Code),
		Title:  fmt.Sprintf("пакет %s (%s)", p.Name, rel),
		URL:    rel,
		Intro:  strings.TrimSpace(p.Doc),
	}
	if d.Intro == "" {
		d.Intro = fmt.Sprintf("Пакет %s без doc-комментария. Файлы: %s.", p.Name, strings.Join(goFiles(pkg), ", "))
	} else {
		d.Intro += "\n\nФайлы: " + strings.Join(goFiles(pkg), ", ") + "."
	}
	sections := []corpus.Section{}
	if len(p.Types) > 0 {
		sections = append(sections, corpus.Section{Path: []string{codeTypes}, Title: codeTypes, Level: 2})
		for _, t := range p.Types {
			var b strings.Builder
			b.WriteString(signature(fset, t.Decl))
			if s := firstParagraph(t.Doc); s != "" {
				b.WriteString("\n\n" + s)
			}
			var methods []string
			for _, f := range append(append([]*doc.Func{}, t.Funcs...), t.Methods...) {
				line := signature(fset, f.Decl)
				if s := firstSentence(f.Doc); s != "" {
					line += " — " + s
				}
				methods = append(methods, line)
			}
			if len(methods) > 0 {
				b.WriteString("\n\n" + strings.Join(methods, "\n"))
			}
			sections = append(sections, corpus.Section{Path: []string{codeTypes, t.Name}, Title: t.Name, Level: 3, Text: b.String()})
		}
	}
	if len(p.Funcs) > 0 {
		sections = append(sections, corpus.Section{Path: []string{codeFuncs}, Title: codeFuncs, Level: 2})
		for _, f := range p.Funcs {
			text := signature(fset, f.Decl)
			if s := firstParagraph(f.Doc); s != "" {
				text += "\n\n" + s
			}
			sections = append(sections, corpus.Section{Path: []string{codeFuncs, f.Name}, Title: f.Name, Level: 3, Text: text})
		}
	}
	d.Sections = sections
	return d, true, nil
}

func goFiles(pkg *ast.Package) []string {
	var names []string
	for p := range pkg.Files {
		names = append(names, filepath.Base(p))
	}
	sort.Strings(names)
	return names
}

// signature — объявление без комментариев и тела функции; длинные
// (структуры с десятками полей) обрезаются до maxSigLines строк.
func signature(fset *token.FileSet, decl ast.Node) string {
	const maxSigLines = 40
	switch n := decl.(type) {
	case *ast.FuncDecl:
		c := *n
		c.Body, c.Doc = nil, nil
		decl = &c
	case *ast.GenDecl:
		c := *n
		c.Doc = nil
		specs := make([]ast.Spec, 0, len(c.Specs))
		for _, s := range c.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok {
				t := *ts
				t.Doc, t.Comment = nil, nil
				specs = append(specs, &t)
			} else {
				specs = append(specs, s)
			}
		}
		c.Specs = specs
		decl = &c
	}
	var b bytes.Buffer
	// Комментарии не передаются (пустой FileSet-узел печатается без них).
	if err := (&printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 4}).Fprint(&b, fset, decl); err != nil {
		return ""
	}
	s := strings.TrimSpace(b.String())
	lines := strings.Split(s, "\n")
	if len(lines) > maxSigLines {
		lines = append(lines[:maxSigLines], "\t// …")
		s = strings.Join(lines, "\n")
	}
	return s
}

// firstParagraph — первый абзац doc-комментария (до пустой строки).
func firstParagraph(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}

// firstSentence — первое предложение doc-комментария.
func firstSentence(s string) string {
	s = firstParagraph(s)
	for i := 0; i < len(s); i++ {
		if s[i] == '.' && (i+1 == len(s) || s[i+1] == ' ') {
			return s[:i+1]
		}
	}
	return s
}

// psDoc — документ scripts/aitr.ps1: Intro — комментарий шапки файла,
// разделы — функции с комментарием (ParsePS) и строкой param(...).
func psDoc(text, rel string) corpus.Doc {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\uFEFF")
	lines := strings.Split(text, "\n")
	var head []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "#") {
			if t == "" && len(head) == 0 {
				continue
			}
			break
		}
		head = append(head, strings.TrimSpace(strings.TrimLeft(t, "#")))
	}
	d := corpus.Doc{
		Schema: corpus.Schema,
		ID:     rel,
		Source: string(Code),
		Title:  "скрипт " + rel,
		URL:    rel,
		Intro:  strings.TrimSpace(strings.Join(head, "\n")),
	}
	if d.Intro == "" {
		d.Intro = "Скрипт " + rel + " без комментария в шапке."
	}
	funcs := ParsePS(text)
	d.Sections = []corpus.Section{}
	if len(funcs) > 0 {
		d.Sections = append(d.Sections, corpus.Section{Path: []string{codeFuncs}, Title: codeFuncs, Level: 2,
			Text: fmt.Sprintf("Функций: %d.", len(funcs))})
	}
	for _, f := range funcs {
		var b strings.Builder
		fmt.Fprintf(&b, "function %s {", f.Name)
		if p := psParam(lines, f.Line); p != "" {
			b.WriteString("\n    " + p)
		}
		b.WriteString("\n}")
		if f.Doc != "" {
			b.WriteString("\n\n" + f.Doc)
		}
		fmt.Fprintf(&b, "\n\n(%s, строка %d)", rel, f.Line)
		d.Sections = append(d.Sections, corpus.Section{Path: []string{codeFuncs, f.Name}, Title: f.Name, Level: 3, Text: b.String()})
	}
	return d
}

// psParam — строка param(...) функции, объявленной на строке line (с 1):
// на той же строке или в первых строках тела.
func psParam(lines []string, line int) string {
	for i := line - 1; i < len(lines) && i < line+3; i++ {
		t := strings.TrimSpace(lines[i])
		if j := strings.Index(t, "param("); j >= 0 {
			t = t[j:]
			if k := strings.LastIndex(t, ")"); k >= 0 {
				t = t[:k+1]
			}
			return t
		}
		if i >= line && (t == "}" || strings.HasPrefix(t, "function ")) {
			break
		}
	}
	return ""
}
