# Инструкция для подключения агента

> **Редакционная пометка** (2026-10-10): этот документ описывает legacy
> `examples/agent` reference host и standalone Telepathy 0.8.0 baseline integration.
> Это НЕ canonical bootstrap новой агентской сессии в R143-модели.
> Canonical AgentSession, ApplyAgentCommand и production command path реализованы
> в store model, но production CLI/Telepathy caller paths ещё не подключены.
> Новые product integrations не должны расширять legacy path новыми
> workflow-функциями. — FD, legacy/canonical разделение уточнено.

`coord` содержит Go-библиотеку, примеры и самостоятельный Telepathy host/worker.
Подключение произвольной AI-сессии требует явно настроенного wrapper. Ниже два
способа подключения. Установленный Telepathy обеспечивает канал сообщений;
checkpoint, дедупликацию исполнения, writer lock и остановку процесса обеспечивает
legacy `examples/agent` reference host.

## Статус описываемого контура

`examples/agent`, legacy `coordinator.Handle`, standalone Telepathy host/worker
и путь `TASK → ACK → STARTED → RESULT_READY → RESULT → RESULT_ACK` относятся к
**legacy Run execution protocol**. Они не являются production canonical Task
command path.

Canonical `ApplyAgentCommand` реализован на store model, но production CLI и
Telepathy caller paths ещё не подключены. Новые product integrations не должны
расширять legacy path новыми workflow-функциями.

## Запуск из COORD_GLOBAL_ROOT

Все агентские frontend-сессии запускаются из `COORD_GLOBAL_ROOT`.
Назначенный Git checkout или worktree передаётся отдельно через WorkspaceBinding
и не является стартовым `cwd` сессии.

Перед подключением должны быть определены:
- `COORD_GLOBAL_ROOT`;
- `COORD_PROJECT_ID` и `COORD_PROJECT_ROOT`;
- `COORD_REPOSITORY_ROOT` и `COORD_ASSIGNED_WORKTREE`;
- `COORD_SESSION_ID` и `COORD_SESSION_GENERATION`.

Команды репозитория следует выполнять явно через path:
```sh
git -C "$COORD_ASSIGNED_WORKTREE" status --short
make -C "$COORD_ASSIGNED_WORKTREE" check
```

Перед выполнением assignment агент проверяет:
1. ProjectID и integration HEAD;
2. свою роль;
3. Task и base SHA;
4. назначенный worktree;
5. разрешённые файлы;
6. stop conditions.

Старая память модели и старый transport assignment не являются authority.

## Текст, который можно дать агенту

Скопируйте этот текст агенту, который будет настраивать подключение. Замените
WORKTREE, COORDINATOR_SESSION и EXECUTOR_SESSION на назначения вашей установки.

> Используй локальный проект coord для координации задач. Прочитай его README,
> AGENTS.md, docs/AGENT_INSTRUCTIONS.md, docs/ARCHITECTURE.md и docs/OPERATIONS.md.
> Рабочая копия задачи: WORKTREE. Изменения разрешены только в назначенной
> рабочей копии; выполнение задачи не даёт права менять coordinator, реестр
> identities или supervisor.
>
> Используй ТОЛЬКО transport и execution mode, указанные в assignment и
> WorkspaceBinding. Если назначенный канал недоступен, сообщи проверенный
> blocker — не переключайся самостоятельно на другой transport или mode.
> Для legacy CLI: используй examples/agent как locальный reference host.
> Для legacy Telepathy 0.8.0 (baseline): прочитай
> [TELEPATHY_HOST.md](TELEPATHY_HOST.md) и настрой cmd/coord-telepathy host/worker.
> Проверь фактические инструменты и аргументы запуска; не придумывай SDK или CLI flags.
> Если bridge или supervisor отсутствует, сообщи, чего не хватает; не объявляй
> подключение работающим по одному PONG.
>
> Координатор: COORDINATOR_SESSION. Исполнитель: EXECUTOR_SESSION.
> Принимай работу только по валидному TASK с полными task/run/attempt/target_sha/fence,
> своим адресом to, назначенным worktree и ещё действующим deadline.
> Перед новым назначением удерживай исключительный writer lock и проверь HEAD.
> Не запускай одну работу повторно по одинаковому task/run/attempt/fence.
> Дубликат TASK после собственных commits должен только восстановить подтверждения,
> а не запускать работу снова или сбрасывать состояние.
>
> Используй executor.Accept для durable ACK, executor.Heartbeat для прогресса
> и executor.Result для результата; сохраняй очередь до отправки.
> Рабочий путь: TASK → ACK → STARTED → RESULT_READY → RESULT → RESULT_ACK.
> Обновляй heartbeat раньше next_update_by, включая периоды ожидания инструмента.
> Прогресс не уменьшается внутри одного fence. BLOCKED должен иметь причину.
> PONG подтверждает только канал и не заменяет heartbeat.
>
> Доставка RESULT не означает завершения. Передай валидный RESULT_ACK в
> executor.Receipt и дождись durable DONE/FAILED в checkpoint.
> После восстановления канала той же сессии используй Replay.
> Новая сессия получает TASK нового fence только после подтверждённой остановки
> прежнего writer; сама не увеличивай attempt или fence.
>
> При ABORT, потере lease, абсолютном deadline или потере владения прекрати
> запись и останови subprocess вместе с дочерними writers. Supervisor подтверждает
> освобождение writer lock. Не считай отправленный сигнал подтверждением остановки.
> Не отправляй RESULT успешным, если критерии задачи не выполнены.
> В результате укажи изменения, проверки и фактические ограничения.

Текст не подменяет wrapper: модель не может обеспечить durable commit или kernel
lock обещанием в чате. Registry registration, pinned keys, роли и supervisor
configuration выполняет уполномоченный оператор или автоматизированный deployment
process согласно действующей политике. Это не становится `OWNER_REQUIRED`
автоматически: MFA, пароль или системный диалог классифицируются как
`HUMAN_ACTION_REQUIRED`. Для новых назначений профили сохраняются как заявленные
параметры, а не как аттестация модели.

## Legacy CLI / subprocess: рабочий локальный пример

`examples/agent` является **legacy reference host** одного subprocess. Он удерживает
writer lock, ведёт legacy coordinator/executor checkpoints, посылает heartbeat,
сохраняет результат и обрабатывает RESULT_ACK. Это не является production
Session Supervisor.

`exit 0` подтверждает только успешное завершение subprocess. Он не является
независимой приёмкой результата и не заменяет Reviewer/Auditor.

Запускать frontend-сессию нужно из `COORD_GLOBAL_ROOT`. Ниже показан минимальный
локальный пример из корня checkout для тестирования самого reference host:

```sh
go run ./examples/agent \
  -worktree . \
  -state "$COORD_PROJECT_ROOT/runtime/legacy-agent" \
  -prompt 'Проверочная задача' \
  -- /usr/bin/printf 'Проверочная задача выполнена'
```

Для настоящего агента замените executable и аргументы после `--` на его
документированный неинтерактивный режим с вводом через stdin:

```sh
go run ./examples/agent \
  -worktree "$COORD_ASSIGNED_WORKTREE" \
  -state "$COORD_PROJECT_ROOT/runtime/legacy-agent/<run-id>" \
  -timeout 10m \
  -heartbeat 15s \
  -prompt 'Выполни задачу в назначенной рабочей копии и проверь результат' \
  -- /absolute/path/to/agent-cli ARGUMENTS_FOR_STDIN_MODE
```

Последняя команда — шаблон: `ARGUMENTS_FOR_STDIN_MODE` нужно заменить на реальные
аргументы именно вашего CLI. Shell не интерпретирует переданные аргументы.
Wrapper запускает команду в указанном Git worktree; репозиторий должен иметь HEAD.

Контракт subprocess:

- stdin — текст задачи, stdout — краткий результат, stderr — диагностика;
- exit 0 соответствует успешному завершению команды, остальные коды — FAILURE;
- stdout/stderr ограничены 64 KiB; большие артефакты сохраняются отдельно;
- команду можно остановить вместе с её Unix process group; она не daemonize/setsid;
- все writers соблюдают один и тот же `.coord-writer.lock`.

Если CLI возвращает exit 0 даже при неудаче самой задачи, нужен wrapper,
преобразующий его реальный результат в корректный exit code. SUCCESS в локальном
примере означает exit 0, а не независимую проверку содержания работы моделью.

Текущий legacy reference host размещает `.coord-writer.lock` в worktree. Это
**legacy limitation**: новые механизмы должны хранить runtime lock metadata внутри
`COORD_PROJECT_ROOT/runtime` или `.scratch`. Не переносите lock без проверки race
semantics. Не удаляйте `.coord-writer.lock` при живом владельце.

State reference host должен находиться вне версионируемого дерева — внутри
`COORD_PROJECT_ROOT/runtime`, как показано в примерах выше.
Каждый запуск примера создаёт новый run и выполняет новую работу. Это reference
host для одного subprocess, без автоматического resume/recovery, ролей,
сетевых listeners и защиты от writers, выходящих из process group.

## Legacy Telepathy 0.8.0: подключение к сообщениям

Telepathy 0.8.0 является проверенной **baseline-версией standalone legacy integration**.
Использование другой версии требует отдельной compatibility verification.
Этот документ сам по себе не разрешает `update-all.mjs` или обновление живых сессий.

Проверенная схема сообщений в `integration/telepathy/main.go` для Telepathy 0.8.0:

```text
BEGIN_COORD_ENVELOPE
{валидный JSON envelope}
END_COORD_ENVELOPE
```

В эксперименте отправка вызывает MCP `send_message` с аргументами `to` и
`message`; получение вызывает `read_messages`. Для действующей установки
сверьте доступные инструменты и их schema. Одного текстового `ACK` недостаточно:
нужен типизированный envelope с корректным payload и correlation.

Bridge должен:

1. Превращать исходящие envelopes в сообщения Telepathy, соблюдая context.
2. Разбирать входящие blocks через `envelope.Decode` и проверять получателя.
3. Проверять подпись через `transport.SignedClient`; transport peer, поле `from`,
   session address и PID не являются доверенной identity сами по себе. Transport
   обязан сформировать проверенный `AuthenticatedFrame`. Legacy `coordinator.Handle`
   не предоставляет полный canonical AgentSession command boundary и не должен
   использоваться как доказательство production admission.
4. Передавать ACK/heartbeat/RESULT в coordinator.Handle, RESULT_ACK в
   executor.Receipt, TASK/ABORT/PING — в сериализованный executor loop.
5. Принимать повторные сообщения без повторного выполнения задачи.
6. Закрывать receive channel при необратимой потере транспорта.

Назначенный executor loop проверяет роль/профиль, выполняет работу в worktree,
сохраняет checkpoints, соблюдает lease и останавливает writer при ABORT.
Смена session address не меняет agent_id и не делает прежнего исполнителя
независимым аудитором.

Для рабочего standalone подключения используйте
[TELEPATHY_HOST.md](TELEPATHY_HOST.md): подписанный MCP bridge и CLI host/worker.
Экспериментальный harness остаётся отдельной проверкой; произвольный LLM frontend
автоматически не подключается. Тест `make test-telepathy`
запускается только с явно настроенным TELEPATHY_SERVER и изолированными сессиями.
Успешный локальный subprocess test не доказывает работу вашей живой Telepathy-сессии.

## Process evidence и остановка

Отправленный сигнал, закрытие transport и `NO_RESPONSE` не доказывают, что
процесс завершён. `NO_RESPONSE` означает только отсутствие ответа в транспорте.
Подтверждение `EXITED` требует supervisor evidence. Proposed session/process states
ADR-0009 не являются текущей product capability.

## Версии и завершение задачи

RESULT_ACK — новое сообщение локального протокола schema_version=1.
Обновляйте coordinator и executor/bridge вместе: старый строгий decoder его
не понимает. Старые snapshots version=1 читаются; новые поля Run отсутствуют
в старых файлах и получают нулевые значения. При replay старого RESULT
coordinator может заново отправить подтверждение, не выполняя работу повторно.

Role registry теперь удерживает исключительный lock до `Registry.Close()`.
Перед повторным открытием той же роли остановите прежнего владельца.
Эта реализация registry locking требует Unix; неподдерживаемая платформа
отклоняется явно.

Перед реальным подключением проверьте приложение-хост по
[VERIFICATION.md](VERIFICATION.md). Работа daemon, конкретный bridge, модель,
неинтерактивные CLI flags и команда supervisor — отдельные части установки.
