# CHANGELOG — куратор курса

## v31 — копия из v30

Копия из `AnimalGuide_Task30` (импорты `…/internal/X` → `aitr/curator/internal/X`), строки кода / тестов:
`llm` 581/502 (+`llmtest`), `tokens` 269/167, `tools` 425/257 (`tool.go`, `envelope.go`), `agent` 970/608,
`features` 595/224 (`features.go` + свой `catalog.go`), `embed` 783/472 (+`embedtest`), `words` 108/70,
`corpus` 202/64, `db` 172/258, `kb` 2006/896, `cite` 1303/542 (из `rag/`), `mcp` 379/353, `consent` 88/37
(из `lifecycle.go`), `embedder/server.py` 216. Итого ≈ 7 900 строк кода и ≈ 4 450 тестов.

Правки в скопированном коде (кроме импортов):

1. `corpus/corpus.go` — убраны `Species`, `Source*`, `Load/Save/WriteLicense`, `Fetcher.Wikipedia` (нет `store.go`, `wikipedia.go`); оставлены `Doc`, `Section`, `Entry`, `Manifest`, `Text`, `Find`, `Normalize`.
2. `kb` — `eval.go` обрезан до типов вопросов и `Covers` (без `CompareOptions/Report`, нет `compare.go`); из тестов вырезаны прогоны по настоящему корпусу (`realDocs`, `TestRealQuestions`, `TestRealCorpusChunks`, `TestFixedSectionMajority`, `TestMidSentenceAndUnion`), `corpus.SourceWikipedia` → `"wiki"`.
3. `tools` — тесты Википедии/GBIF/`Fetcher`/`LocalTools` и пакет `toolstest` удалены; `InjectedExtract` скопирован константой в `tools_test.go` и `agent/runner_test.go`.
4. `features/catalog.go` — свой: `help.rag`, `help.tools`, `review.rag`, `support.context`, `files.gates`, `close.verify` (все `Default: true`, цены — оценки) + `sources.envelope`, `sources.scan` из v30 (их читает `agent.Runner`); `features_test.go` переписан под новые имена (`TestCatalogBlockOrderFollowsStability`, `TestMCPIsTransport` убраны).
5. `mcp/server.go` — убраны `Fetcher`, `WikiBase/GBIFBase`, `Sources`, `HTTPRequests`; `ServerName` = `course`, версия 31.0.0, инструкция и заголовок — про курс; добавлен конверт `tools.Envelope` по `Spec.Untrusted` в `addTool`; `server_test.go` — свой (транспорт в памяти).
6. `cite/cite.go` — без `retrieve`: `Gate(hits, info, minScore)` по порогу индекса; вырезаны `RAGCite`, `SpeciesMismatch`, `speciesMismatch`, `mismatchProblem`, `Names`, `Past`/`withPast`, судья `Support/Judge`; `defaultClarify` — про задание, а не животное.
7. `cite/search.go` — `SearchTool(s, index, k, obs *Issued)` напрямую над `kb.Searcher`, `issued` → `Issued`, без `PipelineTool`; `ToolName` = `docs_search`, `DefaultIndex` = `structure`, описание — про документы курса.
8. `cite/numbers.go` — вырез `parse/tokenize/wordNumbers` из `rag/rule.go` и `clip` из `rag/judge.go` (нужны `numbersMissing`).
9. `consent/consent.go` — `decisionWords`, `Decided`, `negatedBefore`, `fold`; виды решений — свои константы; в `Approve` добавлены «применя», «публику».
9. `tools/envelope.go` — `TrustNote`: «сведения о животных» → «сведения о проекте», «статьи или базы» → «файлов, документации или вывода команд».
10. `agent/agent.go`, `agent/runner.go` — в `Event` добавлено поле `Result` (полный ответ инструмента, в JSON не уходит): проверка «факт из инструмента» сверяется с полным результатом, а `Detail` остаётся обрезанным для журнала.

### Новое в v31 — `/help`: ассистент, понимающий каталог курса

- **`internal/course`** — каталог курса как проект: корень (по `CLAUDE.md` + `scripts/aitr.ps1`), задания из `assignment.json`, реестр и расхождения с ним, последний контрольный прогон из `report.json` edgerec.
- **`internal/gitx`** — git через `os/exec`: ветка, статус, журнал, diff с пределом, изменённые файлы; `Guard` — ограда путей (корень, без `.git`, `.env*`, `*.local`, `INDEX.md`, реестра).
- **`internal/docs`** — markdown каталога → документ с разделами по заголовкам (виды: card, note, handbook, doc, readme, skill, task, template); «схемы» `assignments.md` и `aitr.md` генерирует код; индекс помнит коммит и хеш документов, `Stale` говорит, устарел ли он. Планы и решения по неделям (`docs/предложение-*`, `docs/решение-*`) не индексируются — они описывают контрольные вопросы самого куратора.
- **`internal/ps`** — запуск `aitr` из PowerShell 5.1 (`-NoProfile`, UTF-8), `Fake` для тестов.
- **`internal/project`** — 13 инструментов MCP-сервера `course` (`git_*`, `repo_*`, `read_file`, `assignments_*`, `assignment_get`, `demo_report`, `aitr_doctor`, `docs_search`) и срезы по ролям; тот же срез идёт агенту и в `.mcp.json` корня для Claude Code.
- **`internal/help`** — агент `/help`: `docs_search` кодом до первого запроса, финишёр `help_answer` — проверка цитат (`cite.CheckWith`) плюс факты о состоянии: ветка, статус, путь, число в ответе обязаны быть подстрокой результата инструмента с указанным `call_id` этого хода; после двух отказов — ответ с пометкой «не проверено». Механизмы `help.rag`, `help.tools`.
- **`internal/web`**, `web/index.html` — окно куратора: лента, источники с раскрываемыми цитатами, факты с `call_id`, журнал вызовов по SSE, вкладки дней 32–35 заглушками.
- **`internal/bench`** — стенд: дорожки, отличающиеся одним механизмом (`CheckLanes` из v30), И-14 на `eval/help.json` (24 вопроса), эталон состояния считает код.
- **`cmd/curator`** — `index`, `calibrate` (порог «не знаю» по dev/out-вопросам, при пересечении шкал — перебор с наибольшим перевесом), `help`, `serve`, `mcp`, `bench`, `snapshot` (снимок модуля в корень репозитория задания, `module aitr/NN-slug`).
- Цена хода `/help`: 1–5 запросов к `deepseek-v4-flash`, ≈ $0.003–0.005; индекс 54 документов — 786 чанков, плотный индекс e5 ≈ 4 мин на CPU, BM25 — 0,5 с.
11. `cite/cite.go` — `quoteNorm`: экранированная черта markdown-таблиц `\|` приравнена к `|` (цитаты из таблиц SKILL/README).

## v33 — поддержка пользователей оснастки курса (2026-10-16)

- **`internal/support`** — JSON-CRM (`users.json`, `tickets.json`, события, статусы пишет только код, атомарная запись), инструменты `crm_user`, `crm_ticket`, `crm_search` (данные, не указания), `crm_note` (запись); инструмента смены статуса у модели нет (ИП-7). Блок «Контекст тикета» (механизм `support.context`, ≈250 токенов): пользователь, задание из `assignment.json`, строки `aitr doctor` про ключи и автора коммитов, последний прогон из `report.json`. Агент поддержки: `docs_search` кодом до запроса по теме и тексту тикета, финишёр `support_reply{answer, sources, quotes, action, clarify, note}` — `resolve` только с источником и дословной цитатой, `escalate` только с уточнением, в тексте нет `chunk_id` и значений ключей, `must_not` тикета не нарушен; после принятого ответа код меняет статус (`resolved`/`escalated`) и пишет событие; непроверенный `resolve` тикет не закрывает.
- **`docs/faq.md`** (корень курса) — 24 вопроса об оснастке, индексируется вместе с остальной документацией.
- **`internal/bench/t16.go`** — И-16: дорожки `main` / `noctx` / `same` на копии набора, категории корпус / близнецы / вне документации, пороги по решению.
- **Окно** — вкладка «Тикеты»: список, карточка с контекстом, «Ответить ассистентом» (SSE), «Закрыть», «Передать»; `GET/POST /api/support/tickets…`; `serve -crm dir`. **CLI** — `support -ticket T-08`, `bench -trial 16`. **MCP** — роль `crm`.
- Правка копии v30: нет. Исправлен `TestSnapshot` (имя подставного модуля не должно переписываться снимком).
- Цена: ответ по тикету 1–3 запроса, ≈ $0.002–0.004; стенд ≈ 36 прогонов.

## v32 — AI-ревью PR корня курса в GitHub Actions (2026-10-17)

- **`internal/review`** — разбор unified diff (hunk'и, новая нумерация, переименования, бинарные; `Commentable`), источники PR: `git diff base...head`, файл патча, GitHub API (`Accept: application/vnd.github.diff`), клиент REST с публикацией ревью (inline-комментарии, маркер дедупа по head sha, повтор без inline при отказе GitHub); агент ревью: `docs_search` кодом до запроса (запрос из имён файлов, пакетов, функций diff и слов правил), инструменты `read_file`/`repo_search`/`git_log`, финишёр `review_done` — строка в diff, `rule`/`docs` только с `chunk_id` из выдачи и дословной цитатой, `bug` — со ссылкой на прочитанную строку, `verdict ok` ⇔ находок нет, дубли схлопнуты; механизм `review.rag` выкл → правила без ссылок (контроль); подставные PR `eval/review` (P1 bug, P2–P4 rule, P5 чистый; `TestPlantedApply` держит патчи свежими).
- **`internal/docs/code.go`** — вид `code`: Go-пакеты (`go/parser`, `go/doc`) и функции `scripts/aitr.ps1` как документы индекса (26 документов, 289 чанков).
- **`internal/bench/t15.go`** — И-15: дорожки `main`/`norag`/`same`, «найдено» = файл, |Δ| ≤ 3, вид.
- **Окно** — вкладка «Ревью»: подставные PR и текущая ветка, diff с подсветкой строк находок, находки с цитатами, вердикт, панель последнего прогона Action. **CLI** — `review [-range|-pr|-patch] [-post] [-out]`, `bench -trial 15`.
- **`.github/workflows/review.yml`** (корень курса) — AI review на `pull_request`: сборка, индекс BM25, `review -pr -post`, summary и артефакт; concurrency по PR.
- Цена: ревью подставного PR 1–4 запроса, ≈ $0.002–0.003.

## v34 — файловый агент на правах этапа (2026-10-17)

- **`internal/files`** — этапы `plan` → согласие (`consent.Decided` или `-yes`) → `apply` → отчёт; на плане только чтение и поиск, инструменты записи `write_file`/`apply_patch`/`git_commit` появляются после согласия и действуют только на файлы плана (ИП-7); запись — в worktree `sandbox/agent/<run>` на ветке `agent/<run>` (`gitx/worktree.go`), разрешённые каталоги и запреты — в `types.go`; финишёры `plan_done` и `files_done` с проверками кодом (файлы существуют или новые, пути разрешены; изменённые считает git и сверяет с заявленными, при записи ≥ 2 файлов); механизм `files.gates` выкл → все инструменты сразу, запрет словами (контроль), счётчик записей до согласия.
- **`eval/files`** — сценарии F1–F4 с проверками (`contains`, `absent`, `exists`, `regex`, `diff_within`, `quotes_verbatim`, `coverage`) и `planted/` — карточки с посаженными нарушениями для F3.
- **`internal/bench/t17.go`** — И-17 на копии курса (`git init`), дорожки `main`/`nogates`/`same`.
- **Окно** — вкладка «Файлы»: цель → план и права этапа → согласие → отчёт с веткой, файлами и diff (общий рендер с «Ревью»). **CLI** — `files "цель" [-yes]`, `files -script`, `bench -trial 17`.
