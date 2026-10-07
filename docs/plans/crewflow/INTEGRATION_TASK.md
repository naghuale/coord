# План будущей интеграции Coord с Crewflow

**Не входит в текущую рабочую версию. Статус: DRAFT, НЕ РЕАЛИЗОВАНО.**

Отдельная задача: Coord bridge с каноническим состоянием Crewflow

Статус: подготовленная спецификация для orchestrator; issue не создан,
Crewflow не изменён, CF-013A не запускался. Это отдельная задача после standalone
Telepathy host/worker. Она не расширяет контракт CF-013A задним числом.

## Проблема и результат

Рабочий transport и локальный Coord snapshot не создают связь с каноническими
Run/Attempt Crewflow. Нужен adapter, который назначает executor только от имени
уже разрешённого канонического запуска, хранит checkpoint и подтверждает результат
после durable записи принятого результата в Crewflow.

Для задачи, оставшейся PLANNED и не имеющей Run/Attempt, protocol PONG означает
только проверку канала. TASK и исполнение не начинаются до явного канонического
start через разрешённый lifecycle Crewflow.

## Источники и обязательное уточнение целевого checkout

Orchestrator должен назвать целевой repository/worktree, revision и контракт
канонических Run/Attempt. Проверенный checkout auditor/crewflow содержит
internal/run/state.go: State с Number, Revision, Worktree, Attempts;
Attempt с Number, Executor, Session, StartedAt, журналами и исходом.
Отдельного exported type Run с RunID в этой версии не обнаружено.
В отчёте CF-013A используется другой локальный контекст. Нельзя объявлять
их схемы одинаковыми или вводить выдуманный API.

Проверенный Crewflow revision: `5bc491f368ea125218106ae344485784c92b7d76`.

Проверенные точки для текущего checkout: internal/run/state.go
(LoadState, UpdateState/UpdateStateThrough, NextAttempt, Ended, InSession),
internal/run/run.go (Env, Command, Stream, Tick, Now, Process/Alive),
internal/run/checkpoint.go, internal/run/resume.go, internal/run/outcome.go,
internal/run/pending_events.go, docs/DESIGN.md §7/7h/7i.
PendingEvent описывает переход внимания задачи и не содержит run:
его нельзя превращать в RESULT_ACK без отдельного решения модели.

В Coord: telepathy/, internal/telepathyhost/, coordinator.go,
executor/{checkpoint,result,receipt}.go, checkpoint/, store/, watchdog/.
Standalone host уже работает и проверяется make test-telepathy-host.

## Границы реализации

1. Реализовать отдельный opt-in adapter за интерфейсом транспорта/executor Crewflow;
   существующий CLI executor остаётся совместимым. Не запускать реальные задачи
   как побочный эффект тестов/установки.
2. Crewflow единолично создаёт канонические Run/Attempt по своим admission rules.
   Bridge получает их IDs, разрешённый worktree/base SHA, профиль, owner и fence.
   Соответствие Coord task/run/attempt/fence сохраняется атомарно либо через
   transactional outbox с детерминированной сверкой после crash.
   Формат canonical run key выбирается из фактической схемы целевого checkout;
   standalone UUID из examples/agent не используется как замена.
3. Telepathy session reference, agent identity и executor frontend session —
   разные сущности. Pinned Ed25519 key привязывает транспортную session;
   agent_id и независимость AUDITOR проверяет канонический registry/политика.
4. Сохранить один writer: OS supervision, cooperative worktree lock,
   проверенное fencing до смены owner. Retry не создаёт Attempt;
   reassign/recovery соблюдает принятую Crewflow модель Attempt и Coord fence.
   Если их семантики различаются, orchestrator фиксирует mapping до кода.
5. Сериализовать TASK/ACK/heartbeat/RESULT/receipt, проверять всю identity и correlation.
   При отказе admission или несовпадении binding не создавать процесс.
6. RESULT означает отчёт исполнителя, не approval, merge и не доказательство PR.
   Проверку реального результата оставить outcome logic Crewflow.
7. RESULT_ACK отправляется только после durable записи результата **в каноническую
   транзакцию Crewflow**, а не только в Coord runs.json.
   Нынешний coordinator.Handle самостоятельно сохраняет RESULT+receipt в своём Store.
   Его нельзя подключить наивно: интеграция должна предоставить atomic persistence
   boundary или отдельный commit path, исключающий преждевременный receipt.
8. Сохранить immutable result identity и retry outbox. Восстановление между записью
   результата и delivery ACK не должно повторять выполнение/создавать новые attempts.
   Ответ transport isError, duplicate suppression и rate limits не считать receipt.
9. Credentials/provisioning — локальное доверенное действие оператора;
   не читать Keychain/home/network в тестах. Отладочные записи исключают secrets.
10. Обновить operation docs, agent prompt и соответствующую строку REGULATION;
    отсутствующий механизм отмечается missing.

## Приёмка и доказательства

Все сценарии сначала воспроизводятся тестом с fake clock, transport, runner,
canonical store и supervisor; network/home реального пользователя не затрагиваются.

| Сценарий | Ожидаемый результат |
|---|---|
| Только PLANNED, нет canonical Run/Attempt | TASK не отправлен, subprocess не стартует |
| Допущенный canonical start | Одна durable привязка, один Attempt, один writer |
| Повтор TASK/ACK, старый heartbeat | Нет повторного исполнения/отката progress |
| Неверные run/attempt/fence/SHA/owner/подпись | Отказ до изменения canonical state |
| Обычный текстовый PONG либо неверный nonce | Не подтверждает protocol probe |
| Transport delivery без ACK | Не означает acceptance |
| RESULT до RESULT_READY | Отклонён |
| Ошибка записи канонического результата Crewflow | RESULT_ACK не отправлен |
| Crash после canonical commit до ACK | Replay завершает receipt без нового Attempt |
| Crash writer до durable RESULT | Автоматический rerun запрещён до fencing/recovery |
| Потеря/повтор RESULT_ACK | Executor сохраняет RESULT до correlated receipt |
| Неуспешная остановка writer | Новый owner не получает право записи |
| Deadline/ABORT/cancellation | Writer остановлен; состояние и причина сохранены |
| Telepathy rate limit/duplicate suppression | Сохранён outbox; нет ложного DONE |
| SUCCESS subprocess без требуемого результата | Crewflow не объявляет успешный исход задачи |
| Существующий CLI executor | Проходит прежний набор regression tests |

После fake-тестов — отдельный явно разрешённый изолированный live smoke:
реальный Telepathy 0.8.0, тестовые identities/keys, temporary repository,
подставной executor, canonical state **целевой версии Crewflow**, проверенные
снимки до/после. Реальную CF-013A этим smoke не запускать.

Перед PR — проверки по контракту целевого Crewflow checkout: gofmt, vet,
race tests, установленный golangci-lint, go mod tidy -diff, git diff --check.
Новый dependency на локальный Coord требует явного решения packaging:
путь Downloads и временный replace не должны попадать в поставку.

## Не входит в эту задачу

Изменение или запуск CF-013A; merge/approval PR; переписывание existing tasks;
перенос пользовательских credentials; обещание exactly-once внешних side effects;
автоматическая интеграция всех LLM frontend; публикация пакетов.

## Отчёт исполнения

Revision обоих проектов, canonical binding/mapping, воспроизводимые тесты,
evidence live smoke, изменённая строка REGULATION, остаточные ограничения.
Итог оформляется отдельным PR/задачей по правилам выбранного checkout.
