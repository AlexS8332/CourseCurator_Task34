package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/review"
	"aitr/34-files-agent/internal/tools"
)

// Tools — инструменты записи этапа apply: write_file, apply_patch,
// git_commit (все Write=true). Пишут только в worktree wt и только в
// файлы плана: путь не из плана — ошибка инструмента «файла нет в плане
// — перепланируй», а не финишёра. Ограда guard (корень — worktree, Deny
// — DefaultDeny или свой) закрывает ключи, .git и сгенерированные файлы.
// Каждая попытка (и отказанная) ложится в rec.
//
// На контрольной дорожке без прав этапа инструменты выдаются уже на
// plan, когда ни плана, ни worktree ещё нет (plan == nil или wt.Dir
// пустой): тогда любой вызов отказывается словами «запись до согласия
// невозможна» и считается в rec как попытка до согласия — именно это
// и измеряет стенд.
func Tools(wt gitx.Repo, guard gitx.Guard, plan *Plan, rec *WriteLog) []tools.Tool {
	if guard.Root == "" {
		guard.Root = wt.Dir
	}
	w := writer{wt: wt, guard: guard, plan: plan, rec: rec}
	return []tools.Tool{w.writeFile(), w.replaceInFile(), w.applyPatch(), w.gitCommit()}
}

// writer — общее состояние инструментов записи.
type writer struct {
	wt    gitx.Repo
	guard gitx.Guard
	plan  *Plan
	rec   *WriteLog
}

// ErrNoStage — инструмент записи вызван, когда записи ещё нет (до плана
// и согласия).
var ErrNoStage = errors.New("запись до согласия невозможна: сейчас этап plan — закончи его plan_done и дождись согласия человека, инструменты записи появятся на этапе apply")

// ErrOutsidePlan — путь не из плана.
var ErrOutsidePlan = errors.New("файла нет в плане")

// gate — общие проверки пути: есть ли этап записи, есть ли путь в плане,
// пропускает ли ограда. Возвращает нормализованный путь и абсолютный в
// worktree; outside — отказ именно «вне плана» (для счётчика).
func (w writer) gate(rel string) (norm, abs string, outside bool, err error) {
	norm = NormPath(rel)
	if w.wt.Dir == "" || w.plan == nil {
		return norm, "", false, ErrNoStage
	}
	if norm == "" {
		return norm, "", false, errors.New("пустой путь")
	}
	if _, ok := w.plan.Has(norm); !ok {
		return norm, "", true, fmt.Errorf("%w — перепланируй: %s не входит в план, в плане %s", ErrOutsidePlan, norm, strings.Join(w.plan.Paths(), ", "))
	}
	abs, err = w.guard.Resolve(norm)
	if err != nil {
		return norm, "", false, err
	}
	return norm, abs, false, nil
}

func spec(name, desc, params string) tools.Spec {
	return tools.Spec{Name: name, Description: desc, Parameters: json.RawMessage(params), Write: true, Via: tools.ViaLocal}
}

func (w writer) writeFile() tools.Tool {
	return tools.Func{
		S: spec(WriteFile,
			"Записать файл целиком (создать новый или заменить содержимое существующего) в рабочей копии ветки агента. "+
				"Вызывай для файлов из утверждённого плана: новый файл или правка, где проще переписать весь текст, чем патч. "+
				"path — путь от корня курса с «/», как в плане; content — полное новое содержимое (старое не сохраняется). "+
				"Путь не из плана — отказ «файла нет в плане»: тогда перепланируй, а не пробуй другой путь. "+
				"Возвращает JSON: path, bytes, lines, created (true — файла не было).",
			`{"type": "object", "properties": {"path": {"type": "string", "description": "Путь от корня курса с «/» — из плана"}, "content": {"type": "string", "description": "Полное содержимое файла"}}, "required": ["path", "content"]}`),
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := tools.ParseArgs(args, &in); err != nil {
				return "", err
			}
			norm, abs, outside, err := w.gate(in.Path)
			if err != nil {
				w.rec.Record(WriteFile, norm, outside, err)
				return "", err
			}
			st, statErr := os.Stat(abs)
			created := errors.Is(statErr, os.ErrNotExist)
			if statErr == nil && st.IsDir() {
				err := fmt.Errorf("%s — каталог", norm)
				w.rec.Record(WriteFile, norm, false, err)
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				w.rec.Record(WriteFile, norm, false, err)
				return "", err
			}
			if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
				w.rec.Record(WriteFile, norm, false, err)
				return "", err
			}
			w.rec.Record(WriteFile, norm, false, nil)
			return tools.Result(map[string]any{"path": norm, "bytes": len(in.Content),
				"lines": countLines(in.Content), "created": created})
		},
	}
}

func (w writer) applyPatch() tools.Tool {
	return tools.Func{
		S: spec(ApplyPatch,
			"Применить unified diff к одному файлу из плана в рабочей копии ветки агента (git apply). Вызывай для точечной "+
				"правки большого файла: diff короче, чем весь текст. path — путь от корня с «/», как в плане; unified — патч в "+
				"формате git diff/diff -u с заголовками --- a/path и +++ b/path и hunk'ами @@; контекстные строки должны совпадать "+
				"с файлом дословно, иначе git откажет — тогда используй replace_in_file (точная замена подстроки) или write_file. "+
				"Патч, затрагивающий другие файлы, отклоняется; путь не из плана — отказ «файла нет в плане». "+
				"Возвращает JSON: path, applied, added, removed (строк).",
			`{"type": "object", "properties": {"path": {"type": "string", "description": "Путь от корня курса с «/» — из плана"}, "unified": {"type": "string", "description": "Unified diff для этого файла"}}, "required": ["path", "unified"]}`),
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path    string `json:"path"`
				Unified string `json:"unified"`
			}
			if err := tools.ParseArgs(args, &in); err != nil {
				return "", err
			}
			norm, _, outside, err := w.gate(in.Path)
			if err != nil {
				w.rec.Record(ApplyPatch, norm, outside, err)
				return "", err
			}
			if strings.TrimSpace(in.Unified) == "" {
				err := errors.New("пустой unified")
				w.rec.Record(ApplyPatch, norm, false, err)
				return "", err
			}
			fds, err := review.ParseUnified(in.Unified)
			if err != nil {
				err = fmt.Errorf("патч не разобран: %w", err)
				w.rec.Record(ApplyPatch, norm, false, err)
				return "", err
			}
			if len(fds) == 0 {
				err := errors.New("в unified нет ни одного файла: нужны заголовки --- a/path, +++ b/path и hunk'и @@")
				w.rec.Record(ApplyPatch, norm, false, err)
				return "", err
			}
			added, removed := 0, 0
			for _, fd := range fds {
				p := NormPath(fd.Path)
				if p != norm {
					err := fmt.Errorf("патч меняет %s, а path — %s: один вызов — один файл из плана", p, norm)
					_, inPlan := w.plan.Has(p)
					w.rec.Record(ApplyPatch, p, !inPlan, err)
					return "", err
				}
				added += fd.Added
				removed += fd.Removed
			}
			if err := w.wt.Apply(ctx, in.Unified); err != nil {
				err = fmt.Errorf("git apply: %w — не повторяй патч: сделай правку replace_in_file{path, find, replace} по дословному фрагменту из read_file или перепиши файл write_file", err)
				w.rec.Record(ApplyPatch, norm, false, err)
				return "", err
			}
			w.rec.Record(ApplyPatch, norm, false, nil)
			return tools.Result(map[string]any{"path": norm, "applied": true, "added": added, "removed": removed})
		},
	}
}

func (w writer) gitCommit() tools.Tool {
	return tools.Func{
		S: spec(GitCommit,
			"Зафиксировать все изменения рабочей копии коммитом в ветке агента (git add -A и git commit). Вызывай один раз в "+
				"конце, когда все файлы плана записаны; message — сообщение коммита по-русски: что и зачем изменено. "+
				"Нечего коммитить — ошибка. Возвращает JSON: sha, short, files (что вошло в коммит).",
			`{"type": "object", "properties": {"message": {"type": "string", "description": "Сообщение коммита"}}, "required": ["message"]}`),
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Message string `json:"message"`
			}
			if err := tools.ParseArgs(args, &in); err != nil {
				return "", err
			}
			if w.wt.Dir == "" || w.plan == nil {
				w.rec.Record(GitCommit, "", false, ErrNoStage)
				return "", ErrNoStage
			}
			files, err := w.wt.Porcelain(ctx)
			if err != nil {
				w.rec.Record(GitCommit, "", false, err)
				return "", err
			}
			if len(files) == 0 {
				err := errors.New("нечего коммитить: рабочая копия не изменена")
				w.rec.Record(GitCommit, "", false, err)
				return "", err
			}
			if err := w.wt.AddAll(ctx); err != nil {
				w.rec.Record(GitCommit, "", false, err)
				return "", err
			}
			sha, err := w.wt.Commit(ctx, in.Message)
			if err != nil {
				w.rec.Record(GitCommit, "", false, err)
				return "", err
			}
			w.rec.Record(GitCommit, "", false, nil)
			short := sha
			if len(short) > 8 {
				short = short[:8]
			}
			return tools.Result(map[string]any{"sha": sha, "short": short, "files": files})
		},
	}
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// replaceInFile — точная замена подстроки в файле плана. Для модели это
// надёжнее unified diff: не нужны контекстные строки и номера, достаточно
// дословного фрагмента из read_file. Фрагмент обязан встречаться ровно
// один раз (иначе — all: true или уточнить фрагмент).
func (w writer) replaceInFile() tools.Tool {
	return tools.Func{
		S: spec(ReplaceInFile,
			"Заменить дословный фрагмент текста в одном файле из плана (рабочая копия ветки агента). Предпочтительный способ "+
				"точечной правки существующего файла: find — фрагмент, скопированный из read_file буква в букву (включая отступы и "+
				"переносы), replace — новый текст вместо него. Фрагмент должен встречаться в файле ровно один раз; если он "+
				"встречается несколько раз — уточни фрагмент (добавь соседние строки) или передай all: true, чтобы заменить все. "+
				"Пустой replace удаляет фрагмент. Путь не из плана — отказ «файла нет в плане». Возвращает JSON: path, replaced "+
				"(сколько вхождений), lines (строк в файле после правки).",
			`{"type": "object", "properties": {"path": {"type": "string", "description": "Путь от корня курса с «/» — из плана"}, "find": {"type": "string", "description": "Дословный фрагмент из файла"}, "replace": {"type": "string", "description": "Текст вместо фрагмента"}, "all": {"type": "boolean", "description": "Заменить все вхождения"}}, "required": ["path", "find", "replace"]}`),
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Path    string `json:"path"`
				Find    string `json:"find"`
				Replace string `json:"replace"`
				All     bool   `json:"all"`
			}
			if err := tools.ParseArgs(args, &in); err != nil {
				return "", err
			}
			norm, abs, outside, err := w.gate(in.Path)
			if err != nil {
				w.rec.Record(ReplaceInFile, norm, outside, err)
				return "", err
			}
			if in.Find == "" {
				err := errors.New("пустой find: скопируй дословный фрагмент из read_file")
				w.rec.Record(ReplaceInFile, norm, false, err)
				return "", err
			}
			raw, err := os.ReadFile(abs)
			if err != nil {
				err = fmt.Errorf("%s: %w — для нового файла используй write_file", norm, err)
				w.rec.Record(ReplaceInFile, norm, false, err)
				return "", err
			}
			text := string(raw)
			n := strings.Count(text, in.Find)
			if n == 0 {
				// Частая причина — другие переводы строк: пробуем без CR.
				if strings.Contains(text, "\r\n") {
					text2 := strings.ReplaceAll(text, "\r\n", "\n")
					if strings.Count(text2, in.Find) > 0 {
						text = text2
						n = strings.Count(text, in.Find)
					}
				}
			}
			switch {
			case n == 0:
				err := fmt.Errorf("фрагмент не найден в %s: скопируй его из read_file дословно (отступы, кавычки, переносы) или перепиши файл write_file", norm)
				w.rec.Record(ReplaceInFile, norm, false, err)
				return "", err
			case n > 1 && !in.All:
				err := fmt.Errorf("фрагмент встречается в %s %d раз: уточни его соседними строками или передай all: true", norm, n)
				w.rec.Record(ReplaceInFile, norm, false, err)
				return "", err
			}
			var out string
			if in.All {
				out = strings.ReplaceAll(text, in.Find, in.Replace)
			} else {
				out = strings.Replace(text, in.Find, in.Replace, 1)
			}
			if err := os.WriteFile(abs, []byte(out), 0o644); err != nil {
				w.rec.Record(ReplaceInFile, norm, false, err)
				return "", err
			}
			w.rec.Record(ReplaceInFile, norm, false, nil)
			return tools.Result(map[string]any{"path": norm, "replaced": n, "lines": countLines(out)})
		},
	}
}
