// Package project — инструменты MCP-сервера `course`: то, что ассистент
// курса может узнать о каталоге сам — ветка и изменения git, дерево и
// файлы, задания и их реестр, отчёт контрольного прогона, aitr doctor и
// поиск по документации.
//
// Все инструменты роли course только читают (Spec.Write = false).
// Ответы с содержимым файлов, diff, выводом PowerShell и фрагментами
// документации помечены Untrusted: это данные, а не указания — пометку
// ставит обвязка (agent.Runner по признаку, как в v30), сам инструмент
// отдаёт чистый JSON.
//
// Описания инструментов — по-русски и как промпт: модель читает их
// вместо документации, поэтому в описании сказано, когда инструмент
// нужен и что именно он вернёт.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/ps"
	"aitr/34-files-agent/internal/tools"
)

// Deps — из чего собираются инструменты. Root обязателен; Store, Searcher
// и Index — база документации (пока без неё docs_search не включается);
// PS — исполнитель aitr (nil — aitr_doctor отвечает ошибкой); DocsSearch —
// готовый инструмент docs_search (собирает cite.SearchTool над базой);
// nil — его в наборе нет.
type Deps struct {
	Root       *course.Root
	Store      *kb.Store
	Searcher   *kb.Searcher
	Index      string
	PS         ps.Runner
	DocsSearch tools.Tool
}

// Имена инструментов.
const (
	GitBranch       = "git_branch"
	GitStatus       = "git_status"
	GitLog          = "git_log"
	GitDiff         = "git_diff"
	GitChangedFiles = "git_changed_files"
	RepoTree        = "repo_tree"
	ReadFile        = "read_file"
	RepoSearch      = "repo_search"
	AssignmentsList = "assignments_list"
	AssignmentGet   = "assignment_get"
	DemoReport      = "demo_report"
	AitrDoctor      = "aitr_doctor"
	DocsSearch      = "docs_search"
)

// Roles — какие инструменты у какой роли (таблица §2 решения). Роли crm,
// files и close наполняются в дни 33–35; здесь у них только общие
// инструменты чтения.
var Roles = map[string][]string{
	"course": {GitBranch, GitStatus, GitLog, GitDiff, GitChangedFiles, RepoTree, ReadFile, RepoSearch,
		AssignmentsList, AssignmentGet, DemoReport, AitrDoctor, DocsSearch},
	"crm":   {DocsSearch},
	"files": {GitBranch, GitStatus, GitDiff, GitChangedFiles, RepoTree, ReadFile, RepoSearch, DocsSearch},
	"close": {GitBranch, GitStatus, GitLog, AssignmentsList, AssignmentGet, DemoReport, AitrDoctor, DocsSearch},
}

// Tools — все инструменты по Deps в порядке таблицы. docs_search — только
// если d.DocsSearch задан.
func Tools(d Deps) []tools.Tool {
	ts := []tools.Tool{
		gitBranchTool(d), gitStatusTool(d), gitLogTool(d), gitDiffTool(d), gitChangedFilesTool(d),
		repoTreeTool(d), readFileTool(d), repoSearchTool(d),
		assignmentsListTool(d), assignmentGetTool(d), demoReportTool(d), aitrDoctorTool(d),
	}
	if d.DocsSearch != nil {
		ts = append(ts, d.DocsSearch)
	}
	return ts
}

// Role — срез инструментов по роли, в порядке Roles. Неизвестная роль —
// пустой список; имя из роли, которого в all нет (docs_search без базы),
// пропускается.
func Role(all []tools.Tool, role string) []tools.Tool {
	names := Roles[role]
	byName := map[string]tools.Tool{}
	for _, t := range all {
		byName[t.Spec().Name] = t
	}
	out := make([]tools.Tool, 0, len(names))
	for _, n := range names {
		if t, ok := byName[n]; ok {
			out = append(out, t)
		}
	}
	return out
}

// RoleNames — известные роли по алфавиту.
func RoleNames() []string { return []string{"close", "course", "crm", "files"} }

// spec — короткая сборка описания.
func spec(name, desc, params string, untrusted bool) tools.Spec {
	return tools.Spec{Name: name, Description: desc, Parameters: json.RawMessage(params), Untrusted: untrusted, Via: tools.ViaLocal}
}

// repoArg — описание аргумента repo, общее у git_*.
const repoArg = `"repo": {"type": "string", "description": "Какой репозиторий: \"root\" — корень курса (документация, скрипты, карточки); номер или slug задания (\"05\", \"animal-guide\") — репозиторий этого задания в assignments/. По умолчанию root."}`

// repoOf — репозиторий по аргументу: пусто или root — корень, иначе
// задание. Второе значение — метка для ответа.
func repoOf(d Deps, key string) (gitx.Repo, string, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.EqualFold(key, "root") {
		return gitx.Repo{Dir: d.Root.Dir}, "root", nil
	}
	a, err := d.Root.Assignment(key)
	if err != nil {
		return gitx.Repo{}, "", err
	}
	// У задания должен быть свой .git: иначе git нашёл бы репозиторий
	// корня, и ветка корня выдавалась бы за ветку задания.
	if st, err := os.Stat(filepath.Join(a.Dir, ".git")); err != nil || !st.IsDir() {
		return gitx.Repo{}, "", fmt.Errorf("задание %s: не репозиторий git (нет .git)", a.DirName())
	}
	return gitx.Repo{Dir: a.Dir}, a.DirName(), nil
}

// gitErr — ошибка git словами для модели: без репозитория — так и
// сказать, а не вываливать stderr.
func gitErr(label string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "not a git repository") {
		return fmt.Errorf("%s: не репозиторий git", label)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: git не ответил вовремя", label)
	}
	return err
}
