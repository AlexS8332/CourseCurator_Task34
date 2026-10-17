// Package consent — решение человека по словам его реплики (вырез из v30
// internal/lifecycle: decisionWords, Decided, negatedBefore, fold).
//
// Цитата доказывает, что слова были сказаны, но не что они значат (урок
// упражнения 15: «план не нужен, я тебе доверяю» подставлялось согласием).
// Решение должно читаться в словах, и читает его код, а не модель: план
// файлового агента применяется и задание закрывается только после
// Decided(Approve, …) по реплике человека.
package consent

import (
	"strings"
	"unicode"
)

// Виды решений.
const (
	Approve = "approve" // утвердить план, применить
	Accept  = "accept"  // принять результат, закрыть
	Reject  = "reject"  // вернуть на доработку
	Replan  = "replan"  // пересмотреть план
	Pause   = "pause"   // отложить
	Resume  = "resume"  // продолжить
)

// decisionWords — слова решения человека по видам.
var decisionWords = map[string][]string{
	Approve: {"утвержда", "утвердил", "утверждено", "согласен", "согласна", "принима", "одобря", "поехали", "годится",
		"подходит", "устраивает", "начинай", "собирай", "применя", "публику", "да", "ага", "ок", "окей", "давай", "давайте", "верно", "хорошо"},
	Accept: {"принима", "принял", "приняла", "согласен", "согласна", "годится", "подходит", "устраивает", "доволен",
		"довольна", "закрывай", "заверша", "готово", "да", "ага", "ок", "окей", "отлично", "хорошо"},
	Pause:  {"пауза", "паузу", "паузе", "стоп", "останов", "приостанов", "отлож", "подожд", "перерыв", "позже", "завтра", "хватит"},
	Resume: {"продолж", "вернемся", "вернуться", "возвращаемся", "возобнов", "поехали", "дальше", "работаем", "снимай", "сними"},
	Reject: {"доработ", "переделай", "переделать", "поправ", "исправ", "верни", "вернуть", "не принима", "не годится", "не подходит"},
	Replan: {"план", "перепланир", "пересмотр", "поменя", "измени", "заново", "замени", "добавь", "убери"},
}

// Decided — читается ли решение kind в словах человека. Отрицание рядом
// отменяет слово: «не утверждаю» — не согласие. Неизвестный kind — слов
// не требует (true).
func Decided(kind, reply string) bool {
	ws, ok := decisionWords[kind]
	if !ok {
		return true
	}
	folded := " " + fold(reply) + " "
	for _, w := range ws {
		i := strings.Index(folded, " "+w)
		for i >= 0 {
			if !negatedBefore(folded[:i+1]) {
				return true
			}
			next := strings.Index(folded[i+1:], " "+w)
			if next < 0 {
				break
			}
			i += next + 1
		}
	}
	return false
}

// negatedBefore — среди двух последних слов перед позицией есть отрицание.
func negatedBefore(head string) bool {
	ws := strings.Fields(head)
	for i := len(ws) - 1; i >= 0 && i >= len(ws)-2; i-- {
		if ws[i] == "не" || ws[i] == "нет" || ws[i] == "без" {
			return true
		}
	}
	return false
}

// fold — нижний регистр, ё → е, всё, кроме букв и цифр, — пробел.
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == 'ё':
			b.WriteRune('е')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
