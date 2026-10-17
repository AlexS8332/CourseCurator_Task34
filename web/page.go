// Package page — страница окна куратора, встроенная в бинарник: окно
// работает с любого каталога после go build, без файлов рядом.
package page

import _ "embed"

// Index — web/index.html.
//
//go:embed index.html
var Index []byte
