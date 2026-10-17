// Package x — маленький пакет для теста индекса кода: doc-комментарий
// пакета, экспортированный тип с методом, функция и неэкспортированная
// функция, которой в индексе быть не должно.
//
// Второй абзац в индекс не попадает.
package x

import "errors"

// Thing — вещь с именем.
//
// Подробности, которые в сигнатуру не входят.
type Thing struct {
	Name string
	n    int
}

// New — новая вещь с именем name. Пустое имя — ошибка.
func New(name string) (*Thing, error) {
	if name == "" {
		return nil, errors.New("пустое имя")
	}
	return &Thing{Name: name}, nil
}

// Count возвращает счётчик.
func (t *Thing) Count() int { return t.n }

// Version — версия пакета.
// Вторая строка того же абзаца.
//
// Третий абзац — не для индекса.
func Version() string { return "1" }

// hidden не экспортируется.
func hidden() {}
