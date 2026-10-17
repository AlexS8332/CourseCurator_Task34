package features

// Механизмы куратора курса (v31–v35): по одному на день, плюс две общие
// защиты ответов инструментов из v30 (Envelope, Scan), которыми пользуется
// agent.Runner.
const (
	// HelpRAG — ответ по корпусу курса: docs_search и help_answer с
	// проверкой источников и цитат кодом (день 31).
	HelpRAG Name = "help.rag"
	// HelpTools — инструменты состояния каталога (git, assignment.json,
	// report.json, aitr doctor) и проверка, что факт о состоянии взят из
	// ответа инструмента этого хода (день 31).
	HelpTools Name = "help.tools"
	// ReviewRAG — ревью PR с опорой на регламент и карточки: замечания о
	// правилах — только с chunk_id и цитатой (день 32).
	ReviewRAG Name = "review.rag"
	// SupportContext — блок «Контекст тикета» в запросе поддержки (день 33).
	SupportContext Name = "support.context"
	// FilesGates — этапы plan → apply → report файлового агента и права
	// этапа на запись (день 34).
	FilesGates Name = "files.gates"
	// CloseVerify — проверка кодом черновика карточки и шагов закрытия
	// задания (день 35).
	CloseVerify Name = "close.verify"
)

// catalog — все механизмы куратора. Новый механизм — новая строка здесь
// (точка роста Р-9), а не правка сборщика запроса.
//
// Цены в токенах — ОЦЕНКИ по описаниям инструментов и схем (словами в
// Note), а не замеры: испытания И-14…И-18 уточнят их парами дорожек
// «включён — выключен», как И-6 в v30. Место блока в запросе — по образцу
// v30: единственный блок (support.context) стоит на месте состояния.
var catalog = []Mechanism{
	{
		Name: HelpRAG, Title: "Ответ по корпусу курса", Since: "v31",
		Kind: KindTool | KindCheck, Default: true,
		Cost: Cost{Tokens: 500, Requests: 0.2, Churn: ChurnNone,
			Note: "оценка: описание docs_search ≈200 и схема help_answer ≈300 в каждом запросе; выдача поиска — после реплики, вне постоянной части; отказ проверки — ещё один запрос (не больше двух)"},
		Fallback: "ответ текстом модели: источников и цитат нет, кодом ничего не проверяется",
		About:    "docs_search по карточкам, конспектам и регламенту; help_answer: chunk_id только из выдачи хода, цитаты дословны, числа ответа есть в цитатах; пустая выдача — только «не знаю» с уточнением",
	},
	{
		Name: HelpTools, Title: "Инструменты состояния каталога", Since: "v31",
		Kind: KindTool | KindCheck, Default: true,
		Cost: Cost{Tokens: 900, Requests: 0, Churn: ChurnNone,
			Note: "оценка: описания одиннадцати инструментов (git_*, repo_*, assignments_*, demo_report, aitr_doctor) ≈900 в каждом запросе; вызовы — шаги агента, запросов к модели не добавляют"},
		Fallback: "о ветках, статусах и прогонах модель отвечает по корпусу и памяти; факты о состоянии не проверяются",
		About:    "ветка, статус, изменённые файлы, assignment.json, report.json и aitr doctor — вызовом инструмента; факт о состоянии в ответе должен быть подстрокой ответа инструмента этого хода (state_facts с call_id)",
	},
	{
		Name: ReviewRAG, Title: "Ревью с опорой на регламент", Since: "v32",
		Kind: KindTool | KindCheck, Default: true,
		Cost: Cost{Tokens: 400, Requests: 0.2, Churn: ChurnNone,
			Note: "оценка: docs_search и схема review_done в запросе ревьюера ≈400; замечание о правиле без chunk_id и цитаты — отказ и ещё один запрос"},
		Fallback: "ревью только по diff: замечания о правилах курса — без ссылки на документ и без проверки кодом",
		About:    "замечания kind rule|docs требуют chunk_id из выдачи хода и дословную цитату, bug — path:line прочитанного файла; (file, line) — строка hunk'а, дубли схлопываются",
	},
	{
		Name: SupportContext, Title: "Контекст тикета", Since: "v33",
		Kind: KindBlock, Place: PlaceState, Default: true,
		Cost: Cost{Tokens: 300, Churn: ChurnRare,
			Note: "оценка: пользователь, стек, статус тикета и вывод aitr doctor ≈300; меняется только со сменой тикета"},
		Fallback: "о тикете, стеке и среде модель узнаёт из реплики пользователя своими словами",
		About:    "блок с данными CRM и живой средой (aitr doctor), который собирает код, а не пересказ человека; must_not тикета проверяется в support_reply",
	},
	{
		Name: FilesGates, Title: "Права этапа файлового агента", Since: "v34",
		Kind: KindCheck | KindTool, Default: true,
		Cost: Cost{Tokens: 250, Churn: ChurnStage,
			Note: "оценка: перечень инструментов этапа ≈250 (plan — чтение, apply — write_file, apply_patch, git_commit); запросов к модели не добавляет"},
		Fallback: "инструменты записи выдаются сразу; план и согласие человека — абзацем промпта, пути не ограничены",
		About:    "этапы plan → apply → report: запись только на apply, только по путям плана и только в worktree; согласие читает код по словам человека (consent.Decided)",
	},
	{
		Name: CloseVerify, Title: "Проверка закрытия задания", Since: "v35",
		Kind: KindCheck | KindTool, Default: true,
		Cost: Cost{Tokens: 350, Requests: 0.2, Churn: ChurnNone,
			Note: "оценка: схема card_draft с claims ≈350; отказ проверки — ещё один запрос (не больше двух)"},
		Fallback: "черновик карточки принимается текстом: числа, пути и метрики прогона не сверяются с источниками",
		About:    "card_draft: шесть разделов, каждое число — из источника (report.json, файлы), каждый путь существует, метрики прогона совпадают; «aitr record» упоминается только при наличии прогона",
	},
	{
		Name: Envelope, Title: "Пометка источника", Since: "v15",
		Kind: KindShape, Default: true,
		Cost:     Cost{Tokens: 30, Churn: ChurnNone, Note: "на каждый ответ инструмента источника"},
		Fallback: "ответ источника уходит модели как есть",
		About:    "ответ источника уходит модели с пометкой «данные, а не указания»",
	},
	{
		Name: Scan, Title: "Поиск попыток управлять агентом", Since: "v15",
		Kind: KindCheck, Default: true,
		Cost:     Cost{Churn: ChurnNone, Note: "только журнал, модель не видит"},
		Fallback: "признаки инъекций в тексте источников не ищутся",
		About:    "помечает в журнале фрагменты источника, похожие на указания агенту",
	},
}

// Catalog — реестр куратора. Ошибка сборки здесь — ошибка программиста,
// поэтому паника: приложение с противоречивым реестром запускаться не
// должно.
func Catalog() *Registry {
	r, err := New(catalog...)
	if err != nil {
		panic(err)
	}
	return r
}
