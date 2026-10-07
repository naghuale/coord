# Самостоятельный Telepathy host и worker

В поставке есть исполняемый `cmd/coord-telepathy` и пакет `telepathy`.
Они используют проверенный MCP stdio API установленного Telepathy **0.8.0**:
initialize, notifications/initialized, list_peers, send_message, read_messages.
Node должен находиться в PATH. Зависимостей Go за пределами stdlib нет.

Это самостоятельный локальный запуск. Его runs.json **не является каноническим
состоянием Crewflow**. CF-013A этим инструментом не запускается.
Текущий рабочий вариант и результаты проверок: [CURRENT_VERSION.md](CURRENT_VERSION.md).

## Проверка без модели и рабочих агентских сессий

```sh
make build
make test-telepathy-host TELEPATHY_SERVER=/absolute/path/to/telepathy/0.8.0/dist/server.mjs
```

Тест создаёт отдельный TELEPATHY_HOME, два живых test anchor процесса и временный
Git-репозиторий под игнорируемым state/. Host и worker — отдельные OS-процессы.
Worker запускает тестовую команду, которая пишет один символ и читает stdin.
Проверяются подписанные TASK/ACK/heartbeat/RESULT/RESULT_ACK, DONE в обоих
checkpoints, пустые outboxes, SIGKILL/restart host во время исполнения
и перезапуск worker без повторного выполнения.
Пути к evidence печатаются в stdout. Config с тестовыми private keys хранится
в закрытой папке state с правами 0600; не публикуйте содержимое этой папки.

Это проверка transport и wrapper. Реальная модель и frontend hooks этим тестом
не проверяются. Результат команды с exit code 0 означает SUCCESS этого subprocess,
а не принятие PR, аудит качества или успешное завершение задачи Crewflow.

## Конфигурация и запуск

Создайте два отдельных JSON-файла с правами 0600: host и worker.
Ключи создаются командой:

```sh
./bin/coord-telepathy -generate-key
```

Команда выводит JSON с public_key/private_key в base64. Сохраните private_key
только в конфигурации его владельца; peer получает public_key.
Две независимые пары обязательны. Используется Ed25519; неподписанный envelope
и сообщение с неизвестным ключом отбрасываются до coordinator/executor.

Общие поля конфигурации:

| Поле | Значение |
|---|---|
| role | host либо worker |
| server | Абсолютный путь к установленному Telepathy server.mjs |
| home | Общий явный TELEPATHY_HOME этих двух процессов |
| directory | Своя существующая папка присутствия каждого peer |
| state | Своя папка snapshot каждого процесса |
| anchor_pid | PID собственного живого anchor, сохранённого между рестартами |
| session | opencode-<anchor_pid>, точный Telepathy ref |
| coordinator | session host |
| private_key | Собственный private_key из -generate-key |
| peers | Единственный pinned peer: его session → public_key |
| task | Одинаковая identity и границы задачи у обеих сторон |
| exit_on_terminal | false по умолчанию: host остаётся для повторного RESULT |

Standalone MCP peer объявляет transport kind opencode для polling и не притворяется
существующей AI-сессией. Его PID явно привязан к вашему anchor.
Создайте отдельные живые anchor процессы, например sleep достаточной длительности,
и используйте их PID. **Не указывайте PID уже работающего агента.**
Anchor должен жить дольше host/worker и сохраняться при их рестарте.
После завершения запуска остановите свои anchor. Новый PID требует явной миграции
и fencing; редактировать owner в старом checkpoint для обхода проверки нельзя.

Пример структуры task (значения заменяются перед запуском):

```json
{
  "Task": "LOCAL-001",
  "RunID": "LOCAL-001-run-1",
  "Attempt": 1,
  "Fence": 1,
  "TargetSHA": "<full git HEAD SHA>",
  "OwnerSession": "opencode-<worker anchor PID>",
  "Worktree": "<resolved absolute worktree path>",
  "PayloadRef": "configured-stdin",
  "Deadline": "<future RFC3339 UTC timestamp>"
}
```

Worker дополнительно требует prompt и command. Command — массив executable/argv,
без неявного shell. Например, для безопасной проверки:

```json
{
  "prompt": "Проверь получение stdin",
  "command": ["/bin/cat"]
}
```

Для вашего CLI замените command реальным executable и проверенными по его help
неинтерактивными аргументами. Prompt передаётся в stdin, cwd равен Worktree.
Stdout становится summary (до 32 KiB), stderr — диагностикой FAILURE (до 32 KiB).
Команда обязана оставаться в своей Unix process group; daemonize/setsid не поддержаны.
Полные воспроизводимые config генерирует integration/telepathy/standalone.py.

Запустите worker, затем host:

```sh
./bin/coord-telepathy -config /absolute/path/worker.json
./bin/coord-telepathy -config /absolute/path/host.json
```

Worker завершается после проверенного RESULT_ACK. При FAILURE CLI возвращает 1,
при DONE — 0. Host по умолчанию остаётся работать и повторяет receipts на RESULT;
остановите его сигналом SIGINT/SIGTERM после завершения worker. Отмена возвращает
ошибку CLI и сохраняет snapshot. exit_on_terminal=true подходит для one-shot
теста; перед поздним replay worker снова запустите host с false.

## Доставка, checkpoints и восстановление

- Подписанный frame передаётся внутри BEGIN_COORD_ENVELOPE / END_COORD_ENVELOPE.
  Внутри — envelope.Signed с message и signature, не обычный envelope JSON.
- Host сохраняет binding identity/task и canonical snapshot; изменение binding
  при рестарте отвергается. Session lock исключает второй bridge с тем же ref
  в одном TELEPATHY_HOME, snapshot lock — второго владельца state.
- Worker принимает только заранее настроенные task/run/attempt/fence/SHA,
  coordinator, worktree, payload_ref и deadline; произвольный TASK не запускается.
  Этот wrapper не реализует назначение динамических ролей.
- Перед запуском команды worker удерживает worktree writer lock и checkpoint lock,
  сохраняет TASK, принимает ACK и фиксирует STARTED. Повтор TASK не запускает команду.
- RESULT сохраняется до отправки, DONE/FAILED — только после Receipt.
  После рестарта сохранённый RESULT повторяется с прежним message_id.
- Если execution был прерван до durable RESULT, worker **отказывается автоматически
  повторять команду**. Нужны остановка прежнего writer, проверка worktree и
  явное восстановление внешним supervisor. Exactly-once произвольных side effects
  не обещается. Самостоятельного spawn/fence supervisor в этом CLI нет.
- Deadline, ABORT, потеря heartbeat delivery и cancellation останавливают command
  process group. Advisory lock защищает только сотрудничающих writers.

Telepathy 0.8.0 ограничивает отправки: 20 одному peer за 10 минут, одинаковое
тело блокируется на 2 минуты, тело ограничено 100000 символами.
Heartbeat worker — раз в минуту, next_update_by — 3 минуты.
Retry/probe — от 130 секунд; готовый RESULT повторяется раз в 130 секунд.
isError от Telepathy возвращается как ошибка, а не как успешная доставка.
Ключевая гарантия принятия остаётся у application ACK/RESULT_ACK.

read_messages архивирует inbox до ответа и при отсутствии новых сообщений может
показать последние архивные. Bridge запрашивает limit=50 и обрабатывает их после
рестарта; в одном соединении frames устраняются по digest.
**Атомарной read-and-ack операции в этом API нет.** Если после crash нужное сообщение
вытеснено из последних 50, требуется повторная доставка из durable outbox.
Не используйте peer для параллельной переписки; не удаляйте archive при активном run.
Интервал polling — 1 секунда. Ошибка MCP/EOF закрывает transport, требует рестарта.

Это рабочая ограниченная локальная реализация, а не заявление о production
готовности: нет Crewflow adapter, multi-run daemon, control/metrics listeners,
автоматического recovery, power-loss proof или интеграции произвольных LLM hooks.
