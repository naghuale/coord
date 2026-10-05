# Разработка и Git

## Рабочий процесс

```sh
git status --short
git switch -c feat/transport-bridge
# Изменить код и необходимые сценарные тесты.
make check
git diff --check
git diff
git add -- path/to/changed-file.go docs/ROADMAP.md
git diff --cached
git commit -m "feat(transport): add byte transport bridge"
```

Пример имени ветки и commit описывает будущую работу; готового Telepathy bridge
в текущей версии нет. Используйте тему `feat`, `fix`, `docs`, `test` или `refactor`
с компонентом в скобках. В теле commit укажите, что поменялось, почему и как проверено.
При работе с AI укажите используемый инструмент, сохраняя человеческое авторство.

Для логически разных изменений делайте отдельные commits. Commit сохраняет локальную
историю; push и публикация требуют настроенного remote и отдельного решения владельца.

## Что менять вместе с кодом

- Wire protocol: `envelope`, validation, decode tests и README/архитектуру.
- FSM: переходы, запрещённые события и сценарии восстановления.
- Persistence: проверку failed commit, restart, corruption и process ownership.
- Roles: atomic batches, revision CAS, immutable selections и conflict checks.
- Supervisor: fencing contract, cleanup replacement и команды в OPERATIONS.
- CLI: `-help`, таблицу конфигурации и примеры команд.

Не меняйте schema/snapshot без плана совместимости или миграции. Не добавляйте
внешнюю зависимость без конкретной причины. Clock и transport должны оставаться
подменяемыми в тестах. Тесты не обращаются к реальным агентским сессиям.

## Как проверить авторство и развитие

```sh
git log --oneline --decorate
git show --stat HEAD
git show --format=fuller HEAD
git log --follow -- coordinator.go
git blame roles/roles.go
git diff HEAD~1 HEAD
```

Последняя команда применима после появления как минимум двух commits.
История функций видна через `git log -L`, например:

```sh
git log -L :Assign:coordinator.go
```

Планы храните в ROADMAP, завершённые изменения — в CHANGELOG. Указанный в плане
пункт не считается реализованным до появления кода и подтверждающей проверки.
