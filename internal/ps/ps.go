// Package ps — запуск команд aitr из scripts/aitr.ps1 через PowerShell.
//
// aitr — функции PowerShell 5.1, другого пути к ним нет. Скрипт
// подключается dot-source в той же команде, что и вызов, вывод консоли
// переводится в UTF-8 (иначе кириллица из Write-Host приходит в кодировке
// OEM). Таймаут — из контекста: aitr record идёт минуты.
package ps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner — исполнитель команды aitr: args — слова после «aitr»
// («doctor», «05»). Возвращает вывод (stdout и stderr вместе: aitr пишет
// Write-Host) и ошибку, если команда не выполнилась или вернула код ≠ 0 —
// вывод при этом тоже возвращается.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// Aitr — Runner над scripts/aitr.ps1 корня курса.
func Aitr(root string) Runner { return aitr{root: root} }

type aitr struct{ root string }

// Shell — какой PowerShell запускать: 5.1 (powershell.exe), как в
// SKILL.md; pwsh скрипт тоже исполнит, но aitr написан под 5.1.
const Shell = "powershell"

// Command — строка -Command для аргументов: её же печатает журнал.
func Command(root string, args ...string) string {
	script := filepath.Join(root, "scripts", "aitr.ps1")
	words := make([]string, 0, len(args)+1)
	for _, a := range args {
		words = append(words, quote(a))
	}
	return "[Console]::OutputEncoding=[Text.Encoding]::UTF8; . " + quote(script) + "; aitr " + strings.Join(words, " ")
}

// quote — аргумент в одинарных кавычках PowerShell (внутри кавычка
// удваивается): так в aitr уходит ровно то слово, что дали, без разбора
// $переменных и подстановок.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (a aitr) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("не указана команда aitr")
	}
	cmd := exec.CommandContext(ctx, Shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", Command(a.root, args...))
	cmd.Dir = a.root
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.ReplaceAll(out.String(), "\r\n", "\n")
	if err != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("aitr %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return text, fmt.Errorf("aitr %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

// Fake — Runner для тестов: ключ — аргументы через пробел («doctor 05»),
// значение — вывод. Неизвестная команда — ошибка. Calls — что вызывали.
type Fake map[string]string

func (f Fake) Run(ctx context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	out, ok := f[key]
	if !ok {
		return "", fmt.Errorf("ps.Fake: нет ответа для «aitr %s»", key)
	}
	return out, nil
}

// Recorder — Runner, который считает вызовы поверх другого (в тестах —
// проверка кэша aitr_doctor).
type Recorder struct {
	Inner Runner
	Calls []string
}

func (r *Recorder) Run(ctx context.Context, args ...string) (string, error) {
	r.Calls = append(r.Calls, strings.Join(args, " "))
	return r.Inner.Run(ctx, args...)
}
