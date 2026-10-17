// Package corpus — документы базы знаний: структура документа, его
// канонический текст и поиск фрагментов в нём.
//
// Документ — вступление и разделы с путём заголовков. Каноническим текстом
// (Doc.Text) пользуются чанкер (смещения чанков) и проверка разметки
// контрольных вопросов (Find): меняется формат текста — меняются все
// смещения, поэтому формат не трогать.
//
// Кто и откуда собирает документы (markdown каталога курса — internal/docs),
// пакет не знает: он только описывает их.
package corpus

import (
	"strings"
)

// Schema — версия формата документа и манифеста.
const Schema = 1

// PageChars — сколько символов считается «страницей» в отчётах (машинописная
// страница ≈ 1800 знаков).
const PageChars = 1800

// Section — раздел документа. Text — СОБСТВЕННОЕ тело раздела, без текстов
// подразделов: чанкер получил бы один и тот же текст дважды. Раздел, у
// которого собственного текста нет (только подразделы), остаётся с пустым
// Text: он нужен для пути.
type Section struct {
	// Path — путь заголовков от верхнего уровня: ["Грабли", "Сборка"].
	Path  []string `json:"path"`
	Title string   `json:"title"`
	// Level — уровень заголовка: 2, 3, 4.
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// Doc — документ корпуса.
type Doc struct {
	Schema int `json:"schema"`
	// ID — идентификатор, он же имя файла или относительный путь.
	ID     string `json:"doc_id"`
	Source string `json:"source"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	// RevID — ревизия источника на момент снимка; OldURL — постоянная
	// ссылка на неё.
	RevID   int64  `json:"revid,omitempty"`
	OldURL  string `json:"oldid_url,omitempty"`
	Fetched string `json:"fetched_at"`
	License string `json:"license"`
	// Intro — вступление до первого заголовка.
	Intro    string    `json:"intro"`
	Sections []Section `json:"sections"`
}

// IntroTitle — имя «раздела» вступления в метаданных чанков.
const IntroTitle = "Вступление"

// Text — канонический плоский текст документа: вступление, затем каждый
// раздел строкой заголовка «## Путь › Раздел» и собственным телом, блоки
// через пустую строку. Смещения чанков (Chunk.Start/End) и поиск
// фрагментов-доказательств контрольных вопросов считаются по этому тексту.
// Меняется формат — меняются все смещения, поэтому формат не трогать.
func (d Doc) Text() string {
	var b strings.Builder
	b.WriteString(d.Intro)
	for _, s := range d.Sections {
		b.WriteString("\n\n## ")
		b.WriteString(strings.Join(s.Path, " › "))
		if s.Text != "" {
			b.WriteString("\n")
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

// Chars — длина канонического текста в символах (рунах).
func (d Doc) Chars() int { return len([]rune(d.Text())) }

// Entry — строка манифеста.
type Entry struct {
	ID     string `json:"doc_id"`
	File   string `json:"file"`
	Title  string `json:"title"`
	Source string `json:"source"`
	RevID  int64  `json:"revid,omitempty"`
	Chars  int    `json:"chars"`
	SHA256 string `json:"sha256"`
}

// Manifest — оглавление корпуса. CorpusSHA — sha256 канонического JSON
// списка Entries (по doc_id): одно число на весь корпус, его печатает
// каждый отчёт.
type Manifest struct {
	Schema    int     `json:"schema"`
	Entries   []Entry `json:"entries"`
	Chars     int     `json:"chars"`
	Pages     float64 `json:"pages"`
	CorpusSHA string  `json:"corpus_sha"`
}

// Find ищет фрагмент в каноническом тексте после нормализации (регистр,
// ё→е, кавычки «»“”„ → ", тире —– → -, пробельные последовательности → один
// пробел). Возвращает смещение и длину в рунах ИСХОДНОГО текста; -1, если
// фрагмента нет. Этой же нормализацией пользуются проверка разметки
// вопросов и сверка цитат.
func Find(text, fragment string) (start, length int) { return find(text, fragment) }

// Normalize — нормализация, которой пользуется Find.
func Normalize(s string) string { return normalize(s) }
