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

| Приоритет | Работа | Зависит от | Критерий завершения |
|---|---|---|---|
| Выполнено локально | Telepathy 0.8.0 MCP byte bridge и standalone CLI | — | Подписанный полный lifecycle и worker restart проверены make test-telepathy-host; arbitrary frontend и canonical Crewflow отдельно |
| P0-1 | Daemon runtime skeleton | ADR-0001 шаг 6; ADR-0004 RT-01…RT-04; ADR-0005 ST-01…ST-06 | Production executable composition, ownership namespace, lifecycle, control/metrics и process-kill acceptance для текущего Run snapshot; design: [P0-1 Daemon Runtime](plans/p0-daemon/DESIGN.md) |
| P0-2 | Canonical Task state и TaskID adoption | P0-1; ADR-0001 шаг 7; ADR-0003; ADR-0004 RT-06; ADR-0005 ST-01/ST-04/ST-06 | Task aggregate использует `task_<ULID>`; Task↔Run mapping не выводится из `fsm.Run.Task`; Task.Fence authoritative; State+Event+Outbox атомарны; startup fail-closed проверяет Task writer invariants |
| P0-3 | Agent write path через daemon | P0-2; ADR-0001 шаг 11; ADR-0004 RT-04 | Telepathy agent writes проходят daemon command boundary и проверяют TaskID, RunID, Attempt, generation и Task.Fence; delivery не означает acceptance; пути в сообщениях протокола (Worktree, ссылки на артефакты результата) проверяются на принадлежность назначенному worktree; отказ без изменения состояния |
| P0-4 | Production executor lifecycle и writer lock | P0-2, P0-3; ADR-0004 RT-06 | Повторный TASK не запускает второй writer; ABORT/lease loss прекращают writer; неоднозначный restart не повторяет произвольные side effects; у каждой Task свой worktree и writer lock |
| P0-5 | Supervisor конкретной установки | P0-1…P0-4; ADR-0004 RT-03/RT-06; Linux CI с поддерживаемым механизмом изоляции (CI в репозитории пока нет) | Fence/stop proof подтверждает остановку writer до transfer; replacement ждёт TASK; failed spawn очищается; process acceptance пройдена на целевом окружении; supervisor запускает executor в изоляции ОС: запись только в назначенный worktree, приватный `/tmp`, toolchain и кэши — только чтение; доступ вне worktree — только по выданному OWNER праву. Без поддерживаемой изоляции (сейчас — только Linux) executor не запускается, fail-closed (решение owner 2026-10-08); production-конфигурация не может отключить изоляцию; в тестах изоляция подставляется через интерфейс. Гарантии изоляции принимаются только из Linux CI с поддерживаемым backend; зелёные тесты на неподдерживаемой платформе изоляцию не доказывают. В обязательной CI-задаче изоляции недоступность backend — провал теста, а не `t.Skip` (переменная окружения требует изоляцию); пропуск допустим только для macOS/локальных прогонов |
| После P0-3 | Модель интеграции агентов | P0-2, P0-3; ADR-0001 шаги 8, 11, 12 (Startup Card, ADR-0006) | Расширяет существующий `roles.Profile` (умения агента под отдельным термином, не «capability» — это право доступа по ADR-0001) и существующий worker-adapter; профиль выдаёт оператор (P1 «Политика доверия к roles»); используется существующий `envelope`, новый transport-протокол не вводится; вердикты Review/Audit принимаются только явной структурой, без разбора свободного текста |
| Выполнено локально | Подтверждение commit результата | — | RESULT_ACK durable вместе с RESULT; executor.Receipt фиксирует local completion; проверены retry/restart |
| P1 | Crash/chaos integration suite | P0-1…P0-5 | Убийство executor/coordinator/watchdog на каждом шаге не создаёт второго writer |
| P1 | Проверка содержимого checkpoint | не определено | Проверяются hashes файлов/артефактов, а не только base HEAD и diff whitespace |
| P1 | Порядок и replay сообщений | не определено | Определены sequence/epoch правила; поздний heartbeat не откатывает прогресс |
| P1 | Recovery после падения watchdog | не определено | Проверяемое освобождение abandoned claim без опасного автоматического expiry |
| P1 | Политика доверия к roles | не определено | Provisioning keys/agent_id, allowlists capabilities и отзыв сессий |
| P2 | Миграции snapshot/schema | не определено | Старые версии загружаются или отклоняются до side effects с понятной диагностикой |
| P2 | Transactional database | не определено | Durable state/outbox/events и эффективные запросы без полной записи snapshot |
| P2 | Нагрузка и bounded queues | не определено | Замеры на тысячах runs; transport sends не задерживают остальные run deadlines |
| P2 | Наблюдаемость эксплуатации | не определено | Health/readiness, structured logs, dashboards, измеримые SLO |
| Известное ограничение P2 | Рост дедупликации `Run.Seen` | не определено | Карта message_id растёт монотонно и очищается только при recovery, поэтому snapshot и стоимость каждого Update растут на длинных run. Окно или лимит не введены: вытеснение открыло бы задвоение RESULT при повторной доставке, а риск выше цены. Требует отдельного решения вместе с проверкой содержимого checkpoint |
| P2 | Организационная независимость | не определено | AUDITOR проверяется по trust domain/организации, помимо agent_id |

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

Для coordinator host ранее был проверен только in-process reopen Run snapshot;
этот сценарий сам по себе не доказывает process recovery. Теперь добавлен
`coordd` executable и process acceptance на реальной composition: SIGKILL/restart,
отказ второго writer до открытия snapshots, Run/outbox replay до readiness,
сохранение RESULT receipt и SIGTERM shutdown. Это покрывает skeleton P0-1 для
текущего Run snapshot, но не canonical Task state и не production supervisor
stop proof; P0-1 остаётся открытым до общей проверки репозитория и согласования
всех эксплуатационных критериев. Design и проверяемые критерии описаны в
[plans/p0-daemon/DESIGN.md](plans/p0-daemon/DESIGN.md). P0-1 не закрывает P0-2
(Task/Event/Outbox), P0-3 (агентский write path), P0-4 (production executor)
или P0-5 (изоляция ОС и stop proof).

Пакет `taskid` пока является проверенным самостоятельным строительным блоком,
а не активным ограничением идентичности в runtime. Его тесты не подтверждают
валидацию TaskID в Run, snapshot, wire protocol или миграции; это входит в
интеграцию canonical Task aggregate.

Локальный reference host CLI/subprocess находится в examples/agent. Он проверен
с тестовым executable, включая process-group cancellation, и не заменяет
production supervisor для произвольного агента. Исправления и воспроизводимые
сценарии перечислены в VERIFICATION.md.
