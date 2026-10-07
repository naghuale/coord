# Дальнейшее развитие

Это план работ, а не список реализованных функций. Приоритеты учитывают текущие
границы ядра; оценки готовности production здесь намеренно не присваиваются.

Проверенные process-level сценарии вынесены в [эксперименты](EXPERIMENTS.md):
реальный MCP round-trip, P0 owner/fence transfer после остановки writer,
P1 coordinator restart и P2/P3 recovery тестового Python worker. Это не закрывает
production bridge, произвольные legacy subprocess или весь chaos checklist.

UX-1 (срез B реализован локально, A и C — нет): первый сценарий
owner и двух агентов — карточки состояний с раздельными
attachment/reachability/readiness/availability, машинный блокер quota и
`coord tui`; per-context профили коротких команд с привязкой к generation и
честный discover/onboarding не реализованы. Нормативное поведение и критерии
приёмки:
[plans/ux-first-scenario/ALIGNED_UX.md](plans/ux-first-scenario/ALIGNED_UX.md),
план (важно → нужно → как → результат): [plans/ux-first-scenario/PLAN.md](plans/ux-first-scenario/PLAN.md),
задача на реализацию: [plans/ux-first-scenario/UX_TASK.md](plans/ux-first-scenario/UX_TASK.md).
Основание: [UX_AUDIT.md](UX_AUDIT.md).
Срез B включает CLI `session block/unblock`, раздельные карточки и машинный
blocker; это не закрывает критерии полного UX-1 и не заменяет приёмку живых TUI.
Перед `unblock` checkpoint проверяется вручную; CLI/TUI не валидируют и не
фиксируют факт проверки. Это нормативное ручное действие. Автоматическая
проверка содержимого checkpoint исключена из UX-1 решением owner 2026-10-08 и
входит в P1 «Проверка содержимого checkpoint».
`source` блокера пока не аутентифицирует supervisor/integration: API требует
owner capability; scoped service identity остаётся открытой границей.

| Приоритет | Работа | Критерий завершения |
|---|---|---|
| Выполнено локально | Telepathy 0.8.0 MCP byte bridge и standalone CLI | Подписанный полный lifecycle и worker restart проверены make test-telepathy-host; arbitrary frontend и canonical Crewflow отдельно |
| P0 | Production coordinator daemon | Конфигурация, старт/остановка, metrics/control listeners и восстановление snapshot |
| Открытая граница P0 | TaskID adoption | Пакет `taskid` проверяет ULID generation/parsing, но production-код его не вызывает; `fsm.Run.Task` остаётся свободной строкой, Task aggregate/snapshot/protocol не интегрированы |
| P0 | Production executor lifecycle и writer lock | Повторный TASK не запускает работу повторно; ABORT/lease loss прекращают writer |
| P0 | Supervisor конкретной установки | Fence подтверждает остановку writer; replacement ждёт TASK; failed spawn очищается |
| Выполнено локально | Подтверждение commit результата | RESULT_ACK durable вместе с RESULT; executor.Receipt фиксирует local completion; проверены retry/restart |
| P1 | Crash/chaos integration suite | Убийство executor/coordinator/watchdog на каждом шаге не создаёт второго writer |
| P1 | Проверка содержимого checkpoint | Проверяются hashes файлов/артефактов, а не только base HEAD и diff whitespace |
| P1 | Порядок и replay сообщений | Определены sequence/epoch правила; поздний heartbeat не откатывает прогресс |
| P1 | Recovery после падения watchdog | Проверяемое освобождение abandoned claim без опасного автоматического expiry |
| P1 | Политика доверия к roles | Provisioning keys/agent_id, allowlists capabilities и отзыв сессий |
| P2 | Миграции snapshot/schema | Старые версии загружаются или отклоняются до side effects с понятной диагностикой |
| P2 | Transactional database | Durable state/outbox/events и эффективные запросы без полной записи snapshot |
| P2 | Нагрузка и bounded queues | Замеры на тысячах runs; transport sends не задерживают остальные run deadlines |
| P2 | Наблюдаемость эксплуатации | Health/readiness, structured logs, dashboards, измеримые SLO |
| Известное ограничение P2 | Рост дедупликации `Run.Seen` | Карта message_id растёт монотонно и очищается только при recovery, поэтому snapshot и стоимость каждого Update растут на длинных run. Окно или лимит не введены: вытеснение открыло бы задвоение RESULT при повторной доставке, а риск выше цены. Требует отдельного решения вместе с проверкой содержимого checkpoint |
| P2 | Организационная независимость | AUDITOR проверяется по trust domain/организации, помимо agent_id |

Production acceptance: реальный полный lifecycle, restart с сохранением результата,
устойчивость к повторам/потере сообщений, подтверждённое отсутствие двух writers
и эксплуатационная инструкция, проверенная на целевом окружении.

Частичные process-level доказательства P0 executor уже есть у одно-run
Telepathy worker: повторный TASK дедуплицируется, неоднозначный restart не
переисполняется, ABORT и потеря heartbeat renewal останавливают writer process
group; см. [VERIFICATION.md](VERIFICATION.md). Это не закрывает production
executor lifecycle: worker привязан к одной заранее настроенной Run, не подключён
к canonical Task daemon, а произвольные daemonizing subprocess остаются вне его
гарантий.

Для coordinator host добавлен file-backed сценарий повторного открытия Run
snapshot новым Coordinator в том же процессе и освобождения ownership lock после
штатной остановки host; второй store отвергается, пока host удерживает lock
(`daemon.TestDaemonReopensFileBackedRunSnapshotAndReleasesLock`). Это не
проверяет смерть/перезапуск OS-процесса. Самостоятельного daemon executable,
process-kill recovery и canonical Task state пока нет, поэтому P0 production
daemon остаётся открытым.

Пакет `taskid` пока является проверенным самостоятельным строительным блоком,
а не активным ограничением идентичности в runtime. Его тесты не подтверждают
валидацию TaskID в Run, snapshot, wire protocol или миграции; это входит в
интеграцию canonical Task aggregate.

Локальный reference host CLI/subprocess находится в examples/agent. Он проверен
с тестовым executable, включая process-group cancellation, и не заменяет
production supervisor для произвольного агента. Исправления и воспроизводимые
сценарии перечислены в VERIFICATION.md.
