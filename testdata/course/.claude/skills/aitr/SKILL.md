---
name: aitr
description: Работа с заданиями мини-курса через `aitr`.
---

# aitr — инструментарий каталога курса

## Команды

| Команда | Что делает |
|---|---|
| `aitr list [-Status <planned\|in-progress\|done\|blocked>]` | таблица заданий |
| `aitr doctor <номер\|slug>` | проверка среды: метаданные, git, тулчейн, ключи, документация |
| `aitr sync` | пересобирает `INDEX.md` и `docs/registry.json` из `assignment.json` заданий |

## Секреты

Значения из `.env` не печатай.
