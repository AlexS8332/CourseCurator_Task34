package review

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// hunkRe — заголовок hunk'а: «@@ -старт[,строк] +старт[,строк] @@ контекст».
var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)

// ParseUnified разбирает unified diff (вывод `git diff`, `git diff
// --no-index`, GitHub `application/vnd.github.diff` или `diff -u`) на файлы
// и hunk'и. Без внешних зависимостей.
//
// Файл начинается строкой «diff --git a/X b/Y» или, у plain diff, парой
// «--- a/X» / «+++ b/Y». Префиксы a/ и b/ снимаются, «/dev/null» значит
// добавление или удаление. Строки «rename from/to», «new file mode»,
// «deleted file mode», «Binary files … differ» и «GIT binary patch»
// дают статус и признак Binary. Число строк hunk'а берётся из заголовка:
// пока оно не исчерпано, любая строка — тело hunk'а (так «--- x» внутри
// hunk'а не путается с заголовком файла). «\ No newline at end of file»
// пропускается.
//
// Diff, обрезанный посреди hunk'а (FromRange с пределом байт), ошибкой не
// считается: разобранное остаётся, хвоста просто нет. Ошибка — hunk вне
// файла, неразборный заголовок или строка тела с чужим префиксом.
func ParseUnified(diff string) ([]FileDiff, error) {
	var (
		files []FileDiff
		cur   *FileDiff
		hunk  *Hunk
		// Сколько строк старого и нового файла ещё ждёт текущий hunk.
		oldLeft, newLeft int
		oldNo, newNo     int
		// oldSeen/newSeen — путь из «---»/«+++» текущего файла.
		oldSeen, newSeen string
		// inHeader — между «diff --git» и первым «@@» (или бинарной пометкой).
		inHeader bool
	)
	flushHunk := func() {
		if hunk != nil && cur != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
		oldLeft, newLeft = 0, 0
	}
	flushFile := func() {
		flushHunk()
		if cur == nil {
			return
		}
		finishFile(cur, oldSeen, newSeen)
		files = append(files, *cur)
		cur = nil
		oldSeen, newSeen = "", ""
		inHeader = false
	}
	startFile := func() {
		flushFile()
		cur = &FileDiff{Status: StatusModified}
		inHeader = true
	}

	lines := strings.Split(diff, "\n")
	for i, raw := range lines {
		line := strings.TrimSuffix(raw, "\r")
		if i == len(lines)-1 && line == "" {
			break // хвост после последнего «\n»
		}
		// Тело hunk'а: пока заголовок обещает строки.
		if hunk != nil && (oldLeft > 0 || newLeft > 0) {
			if strings.HasPrefix(line, `\`) {
				continue // «\ No newline at end of file»
			}
			kind, text := byte(' '), ""
			if line != "" {
				kind, text = line[0], line[1:]
			}
			switch kind {
			case ' ':
				if oldLeft <= 0 || newLeft <= 0 {
					return nil, fmt.Errorf("строка %d: контекст сверх счёта hunk'а", i+1)
				}
				hunk.Lines = append(hunk.Lines, Line{Kind: ' ', Text: text, Old: oldNo, New: newNo})
				oldNo++
				newNo++
				oldLeft--
				newLeft--
			case '+':
				if newLeft <= 0 {
					return nil, fmt.Errorf("строка %d: добавление сверх счёта hunk'а", i+1)
				}
				hunk.Lines = append(hunk.Lines, Line{Kind: '+', Text: text, New: newNo})
				cur.Added++
				newNo++
				newLeft--
			case '-':
				if oldLeft <= 0 {
					return nil, fmt.Errorf("строка %d: удаление сверх счёта hunk'а", i+1)
				}
				hunk.Lines = append(hunk.Lines, Line{Kind: '-', Text: text, Old: oldNo})
				cur.Removed++
				oldNo++
				oldLeft--
			default:
				return nil, fmt.Errorf("строка %d: в теле hunk'а неожиданный префикс %q", i+1, string(kind))
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			startFile()
			a, b := splitGitPaths(strings.TrimPrefix(line, "diff --git "))
			cur.OldPath, cur.Path = a, b
		case strings.HasPrefix(line, "@@ "):
			if cur == nil {
				return nil, fmt.Errorf("строка %d: hunk вне файла", i+1)
			}
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("строка %d: заголовок hunk'а не разобран: %s", i+1, line)
			}
			flushHunk()
			inHeader = false
			h := Hunk{OldStart: atoi(m[1]), OldLines: 1, NewStart: atoi(m[3]), NewLines: 1, Header: strings.TrimSpace(m[5])}
			if m[2] != "" {
				h.OldLines = atoi(m[2])
			}
			if m[4] != "" {
				h.NewLines = atoi(m[4])
			}
			hunk = &h
			oldLeft, newLeft = h.OldLines, h.NewLines
			oldNo, newNo = h.OldStart, h.NewStart
		case strings.HasPrefix(line, "--- "):
			if cur == nil || !inHeader {
				// plain `diff -u`: пара «---»/«+++» открывает файл.
				startFile()
			}
			oldSeen = stripPrefix(line[4:], "a/")
		case strings.HasPrefix(line, "+++ "):
			if cur == nil {
				return nil, fmt.Errorf("строка %d: «+++» без «---»", i+1)
			}
			newSeen = stripPrefix(line[4:], "b/")
		case cur != nil && inHeader:
			switch {
			case strings.HasPrefix(line, "rename from "):
				cur.OldPath = unquote(strings.TrimPrefix(line, "rename from "))
				cur.Status = StatusRenamed
			case strings.HasPrefix(line, "rename to "):
				cur.Path = unquote(strings.TrimPrefix(line, "rename to "))
				cur.Status = StatusRenamed
			case strings.HasPrefix(line, "new file mode"):
				cur.Status = StatusAdded
			case strings.HasPrefix(line, "deleted file mode"):
				cur.Status = StatusDeleted
			case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
				cur.Binary = true
			}
			// index, similarity index, old/new mode и прочее — не нужны.
		default:
			// Текст между файлами (например, шапка письма из format-patch).
		}
	}
	flushFile()
	for i := range files {
		if files[i].Hunks == nil {
			files[i].Hunks = []Hunk{}
		}
	}
	return files, nil
}

// finishFile сводит пути и статус файла: «---»/«+++» надёжнее строки
// «diff --git» (там пути с пробелами неоднозначны), /dev/null даёт
// добавление и удаление, разные пути без явного rename — переименование.
func finishFile(f *FileDiff, oldSeen, newSeen string) {
	if oldSeen == "/dev/null" {
		f.Status = StatusAdded
		oldSeen = ""
	}
	if newSeen == "/dev/null" {
		f.Status = StatusDeleted
		newSeen = ""
	}
	if f.Status == StatusRenamed {
		if newSeen != "" {
			f.Path = newSeen
		}
		if oldSeen != "" {
			f.OldPath = oldSeen
		}
		return
	}
	if f.Status == StatusAdded {
		if newSeen != "" {
			f.Path = newSeen
		}
		f.OldPath = ""
		return
	}
	if f.Status == StatusDeleted {
		if oldSeen != "" {
			f.Path = oldSeen
		} else if f.OldPath != "" {
			f.Path = f.OldPath
		}
		f.OldPath = ""
		return
	}
	// modified
	if newSeen != "" {
		f.Path = newSeen
	}
	if oldSeen != "" {
		f.OldPath = oldSeen
	}
	if f.OldPath != "" && f.OldPath != f.Path {
		f.Status = StatusRenamed
	} else {
		f.OldPath = ""
	}
	if f.Path == "" {
		f.Path = oldSeen
	}
}

// splitGitPaths делит «a/X b/Y» на X и Y. Пути с пробелами: ищется
// такое деление, при котором левая часть начинается с «a/», правая — с
// «b/», а сами пути совпадают (обычный случай) — иначе первое деление.
func splitGitPaths(s string) (string, string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		// Пути в кавычках (не ASCII при quotepath=on): «"a/X" "b/Y"».
		parts := strings.SplitN(s, `" "`, 2)
		if len(parts) == 2 {
			return stripPrefix(unquote(parts[0]+`"`), "a/"), stripPrefix(unquote(`"`+parts[1]), "b/")
		}
	}
	var first string
	for i := 0; i+1 < len(s); i++ {
		if s[i] != ' ' || !strings.HasPrefix(s[i+1:], "b/") {
			continue
		}
		a, b := stripPrefix(s[:i], "a/"), stripPrefix(s[i+1:], "b/")
		if first == "" {
			first = s[:i]
		}
		if a == b {
			return a, b
		}
	}
	if first != "" {
		i := len(first)
		return stripPrefix(first, "a/"), stripPrefix(s[i+1:], "b/")
	}
	return s, s
}

// stripPrefix снимает «a/» или «b/», кавычки и хвост «\tдата» у plain diff.
func stripPrefix(p, prefix string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	p = unquote(p)
	if p == "/dev/null" {
		return p
	}
	return strings.TrimPrefix(p, prefix)
}

func unquote(p string) string {
	p = strings.TrimSpace(p)
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
		return p[1 : len(p)-1]
	}
	return p
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// Commentable — можно ли оставить inline-комментарий к строке line новой
// нумерации: она добавлена или контекстная в каком-то hunk'е (GitHub
// принимает комментарии только к строкам diff; у бинарного или
// удалённого файла таких нет).
func (f FileDiff) Commentable(line int) bool {
	_, ok := f.LineText(line)
	return ok
}

// LineText — текст строки line новой нумерации, если она есть в diff
// (добавленная или контекстная).
func (f FileDiff) LineText(line int) (string, bool) {
	if line <= 0 {
		return "", false
	}
	for _, h := range f.Hunks {
		if line < h.NewStart || line >= h.NewStart+max(h.NewLines, 1) {
			continue
		}
		for _, l := range h.Lines {
			if l.Kind != '-' && l.New == line {
				return l.Text, true
			}
		}
	}
	return "", false
}

// CommentableLines — все строки файла новой нумерации, к которым можно
// оставить комментарий, по порядку (для отказа «строка N не в diff
// файла X: доступны …»).
func (f FileDiff) CommentableLines() []int {
	var out []int
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind != '-' {
				out = append(out, l.New)
			}
		}
	}
	return out
}

// Ranges — диапазоны комментируемых строк файла вида «12–20, 35–41»
// (для сообщений модели и окна).
func (f FileDiff) Ranges() string {
	var parts []string
	for _, h := range f.Hunks {
		lines := 0
		for _, l := range h.Lines {
			if l.Kind != '-' {
				lines++
			}
		}
		if lines == 0 {
			continue
		}
		if lines == 1 {
			parts = append(parts, strconv.Itoa(h.NewStart))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d–%d", h.NewStart, h.NewStart+lines-1))
	}
	return strings.Join(parts, ", ")
}

// FileByPath — файл diff по пути (разделители любые); nil — такого нет.
func FileByPath(files []FileDiff, p string) *FileDiff {
	p = NormPath(p)
	for i := range files {
		if NormPath(files[i].Path) == p {
			return &files[i]
		}
	}
	return nil
}

// NormPath — путь с «/», без ведущих «./» и «a/»/«b/» git'а.
func NormPath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "./")
	for _, pre := range []string{"a/", "b/"} {
		if strings.HasPrefix(p, pre) && !strings.HasPrefix(p, pre+"/") {
			// «a/tools/x» → «tools/x»; реальные каталоги a и b в корне курса не водятся.
			p = strings.TrimPrefix(p, pre)
			break
		}
	}
	return p
}
