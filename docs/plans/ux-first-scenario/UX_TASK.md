# Задача UX-1: профили контекста, карточки состояний, честный discovery

Статус: срез B одобрен owner и реализован; срезы A и C не начинать без
явного approval owner. Реализация B исправляла дефекты уже написанного среза B
(недостижимый blocker-гейт, 405 на /availability, неразличимые 400/409, гонка
в TUI). Checkpoint-before-unblock остаётся ручным процедурным требованием и
не проверяется программой. Автоматическая проверка checkpoint исключена из
UX-1 решением owner от 2026-10-08; она относится к P1 «Проверка содержимого
checkpoint» в [ROADMAP](../../ROADMAP.md).
Нормативное поведение и критерии: [ALIGNED_UX.md](ALIGNED_UX.md).
Основание: выводы [UX_AUDIT.md](../../UX_AUDIT.md) F1–F3 и отзыв Nemotron (seq56).

## Проблема

Durable sessions и scoped API работают в проверенных сценариях, но первый
живой обмен показал три продуктовых дефекта: ATTACHED маскирует недоступность
провайдера (F1), onboarding требует заранее известные REF и ручные подсказки
(F2), каждая команда тащит absolute пути и credential-file без привязки к
поколению контекста (F3). Реализация не должна ослабить fencing.

## Результат

Три горизонтальных среза, каждый с тестами fake clock/transport до live-прогона:

### Срез A — per-context профиль коротких команд

- Команды: attach (приём приглашения создаёт профиль), profile create/list/
  refresh/remove; флаг --profile для context/ready/say/wait/checkpoint/ack/resume.
- Содержимое профиля: state path (daemon), session ID, connection/context ID,
  pointer на credential-file, generation на момент создания. Файл 0600 под
  XDG config; секрет не копируется, не печатается.
- Привязка к поколению обязательна: resume повышает generation и меняет
  credential; прежний профиль отдаёт структурированную ошибку stale c
  подсказкой refresh. Запрещён любой auto-select newest credential в state.
- Клиент durable сохраняет operation ID до отправки; повтор lost response
  использует тот же ID, новая операция — новый ID.
- Совместимость: старые длинные флаги работают без изменений.

### Срез B — карточка состояния и машинные блокеры

- Раздельные поля: attachment / transport reachability / readiness /
  processing cursor / last activity / provider availability / blocker /
  ожидание «кого-чего-до когда». None из них не выводится из другого.
- blocked_provider_quota — состояние с reason/source/observed_at/retry_at;
  останавливает новые назначения роли, не трогает checkpoint/journal;
  снимается только явно. При отсутствии внешнего сигнала availability=unknown.
- Активные, остановленные и stale-generation сессии выводятся раздельно;
  пустой wait по таймауту — штатный ответ, не ошибка.
- Owner-команды `session block/unblock`; API сейчас использует owner capability.
  `source` — только заявленная provenance-строка, не identity вызывающего.
  Отдельная scoped capability для supervisor/integration остаётся открытой;
  daemon сам сообщает только transport-факты.

### Срез C — discover и onboarding без известных REF

- discover: реальные transport refs, время наблюдения, availability=unknown
  до подтверждения; пустой список объясняет, какие TUI открыть и как дать
  первый prompt.
- init/start карточка: project ID, runtime alias, backend readiness, явные
  расположения source/runtime/log/history.
- prompt/help перестраиваются в порядок первого сценария (peers/discover до
  диагностики); тексты ошибок содержат следующую допустимую команду.
- Короткий runtime alias должен сокращать итоговый socket path; предел
  >=100 символов либо устраняется, либо документируется как известный.

## Границы

- Не менять protocol transport и семантику attempt/fence (AGENTS.md).
- Delivery notification != принятие задачи; ничего не отменяет это правило.
- Coord не обнаруживает quota сам; сигналы только от owner/supervisor/integration.
- F4 (sandbox против полного shell одного OS user) вне этой задачи.
- Не запускать реальные пользовательские сессии как побочный эффект тестов.

## Порядок приёмки (после реализации)

Прогон разделов 4 и 5 ALIGNED_UX.md: новый owner без предварительных REF,
Codex Auditor + Nemotron как живые TUI; LongCat исключён до снятия квоты.
Evidence — в комнате и приватном state, не в source Git. Отчёт фиксирует
revision, пройденные/непройденные критерии и остаточные ограничения.

## Проверки перед PR

make check, go vet, go test -race -count=1 ./..., golangci-lint,
go mod tidy -diff, git diff --check; новые сценарные тесты на fake clock/
transport; make test-sessions и test-telepathy-host остаются зелёными.
Обновить SESSION_CLI.md, QUICKSTART.md, README, CHANGELOG при изменении CLI.
