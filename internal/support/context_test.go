package support

import (
	"context"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/ps"
	"aitr/34-files-agent/internal/tokens"
)

func TestGatherAndBlock(t *testing.T) {
	d, s := deps(t), testStore(t)
	rec := &ps.Recorder{Inner: d.PS}
	d.PS = rec
	c, err := Gather(context.Background(), d, s, "T-08")
	if err != nil {
		t.Fatal(err)
	}
	if c.User.ID != "u-agent-31" || c.Assignment == nil || c.Assignment.Num != "31" || c.LastRun == nil || c.LastRun.Failed == nil {
		t.Fatalf("контекст: %+v", c)
	}
	block := c.Block()
	for _, w := range []string{BlockTitle, "T-08", "задание 31", "стек go", "Claude Code в задании 31 (агент Claude Code)", "Windows 10, PowerShell 5.1",
		"есть gh, go, uv", "нет node", "31-course-help", "нужны ключи DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY - не задан", "автор коммитов: AlexS8332",
		"не пройден, проверок 2/3", "упал шаг 3 (expect .msg.assistant: элемент не появился за 10s)", "js_errors 1: Failed to load resource: 401"} {
		if !strings.Contains(block, w) {
			t.Errorf("в блоке нет %q:\n%s", w, block)
		}
	}
	for _, w := range []string{"go version", "карточка:", "рабочее дерево", "stack = go"} {
		if strings.Contains(block, w) {
			t.Errorf("в блоке лишняя строка doctor %q:\n%s", w, block)
		}
	}
	if n := int(tokens.Default.Text(block)); n > 320 {
		t.Errorf("блок ≈ %d токенов, ожидалось около 200:\n%s", n, block)
	}
	// Второй сбор — из кэша: aitr doctor не запускается заново.
	if _, err := Gather(context.Background(), d, s, "T-08"); err != nil {
		t.Fatal(err)
	}
	if len(rec.Calls) != 1 || rec.Calls[0] != "doctor 31" {
		t.Errorf("вызовы aitr: %v", rec.Calls)
	}
}

func TestGatherWithoutRootOrDoctor(t *testing.T) {
	s := testStore(t)
	c, err := Gather(context.Background(), project.Deps{}, s, "T-10")
	if err != nil {
		t.Fatal(err)
	}
	if c.Assignment != nil || c.Doctor != "" || c.LastRun != nil {
		t.Fatalf("без корня задания и doctor быть не должно: %+v", c)
	}
	block := c.Block()
	if !strings.Contains(block, "Дмитрий (однокурсник), macOS, zsh") || !strings.Contains(block, "нет gh") || !strings.Contains(block, "стек python") {
		t.Errorf("блок без корня: %s", block)
	}
	if strings.Contains(block, "aitr doctor") || strings.Contains(block, "прогон") {
		t.Errorf("без задания строки doctor и прогона лишние: %s", block)
	}
	// Задание есть, doctor недоступен (PS nil) — блок говорит об этом.
	d := deps(t)
	d.PS = nil
	c, err = Gather(context.Background(), d, s, "T-08")
	if err != nil {
		t.Fatal(err)
	}
	if c.Doctor != "" || !strings.Contains(c.Block(), "aitr doctor не запускался") {
		t.Errorf("без PS: %q", c.Block())
	}
	// Неизвестный тикет — ошибка.
	if _, err := Gather(context.Background(), d, s, "T-99"); err == nil {
		t.Error("ожидалась ошибка «тикет не найден»")
	}
}

func TestDoctorLines(t *testing.T) {
	lines := DoctorLines(doctorOut)
	want := []string{"автор коммитов: AlexS8332", "корневой .env: ключей 3", "FAIL DEEPSEEK_API_KEY - не задан"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("строки doctor: %q", lines)
	}
	if got := DoctorLines("  ok    задание не требует ключей\n  warn  автор коммитов: Alex - ожидается AlexS8332\n  ok    DEEPSEEK_API_KEY - задан в корневом .env\n"); len(got) != 3 {
		t.Errorf("%q", got)
	}
	if got := DoctorLines(""); len(got) != 0 {
		t.Errorf("пустой вывод: %q", got)
	}
}
