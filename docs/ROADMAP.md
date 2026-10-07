# Дальнейшее развитие

Это план работ, а не список реализованных функций. Приоритеты учитывают текущие
границы ядра; оценки готовности production здесь намеренно не присваиваются.

Проверенные process-level сценарии вынесены в [эксперименты](EXPERIMENTS.md):
реальный MCP round-trip, P0 owner/fence transfer после остановки writer,
P1 coordinator restart и P2/P3 recovery тестового Python worker. Это не закрывает
production bridge, произвольные legacy subprocess или весь chaos checklist.

| Приоритет | Работа | Критерий завершения |
|---|---|---|
| Выполнено локально | Telepathy 0.8.0 MCP byte bridge и standalone CLI | Подписанный полный lifecycle и worker restart проверены make test-telepathy-host; arbitrary frontend и canonical Crewflow отдельно |
| P0 | Production coordinator daemon | Конфигурация, старт/остановка, metrics/control listeners и восстановление snapshot |
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
| P2 | Организационная независимость | AUDITOR проверяется по trust domain/организации, помимо agent_id |

Production acceptance: реальный полный lifecycle, restart с сохранением результата,
устойчивость к повторам/потере сообщений, подтверждённое отсутствие двух writers
и эксплуатационная инструкция, проверенная на целевом окружении.

Локальный reference host CLI/subprocess находится в examples/agent. Он проверен
с тестовым executable, включая process-group cancellation, и не заменяет
production supervisor для произвольного агента. Исправления и воспроизводимые
сценарии перечислены в VERIFICATION.md.
