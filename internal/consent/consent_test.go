package consent

import "testing"

// Решение читается в словах; отрицание рядом отменяет слово; неизвестный
// вид решения слов не требует.
func TestDecided(t *testing.T) {
	cases := []struct {
		kind, reply string
		want        bool
	}{
		{Approve, "Да, применяй.", true},
		{Approve, "План утверждаю, поехали", true},
		{Approve, "Нет, не надо", false},
		{Approve, "Не утверждаю — переделай", false},
		{Approve, "План не нужен, я тебе доверяю", false},
		// Отрицание в двух словах перед словом решения отменяет его —
		// консервативно: «не надо, применяй» не читается как согласие.
		{Approve, "Уточнять не надо, применяй", false},
		{Approve, "Не надо уточнять, применяй", true},
		{Accept, "Готово, закрывай задание", true},
		{Accept, "Без принятия, верни на доработку", false},
		{Reject, "Верни на доработку", true},
		{Replan, "Поменяй план: добавь тесты", true},
		{Pause, "Давай отложим до завтра", true},
		{Resume, "Продолжаем", true},
		{"unknown", "что угодно", true},
	}
	for _, c := range cases {
		if got := Decided(c.kind, c.reply); got != c.want {
			t.Errorf("Decided(%s, %q) = %v, ждали %v", c.kind, c.reply, got, c.want)
		}
	}
	if fold("Ёж — да!") != "еж да" {
		t.Fatalf("fold: %q", fold("Ёж — да!"))
	}
}
