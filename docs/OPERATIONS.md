# Эксплуатация и команды

Текущая поставка содержит библиотеку coordinator, host runtime из пакета
`daemon`, локальное демо и отдельный watchdog CLI. Есть ограниченный
самостоятельный Telepathy host/worker ([TELEPATHY_HOST.md](TELEPATHY_HOST.md)).
Отдельного daemon executable и canonical Task runtime пока нет: приложение-хост
предоставляет transport и создаёт Coordinator для `daemon`.

## Значения по умолчанию

| Настройка | Значение | Где задаётся |
|---|---|---|
| ACK timeout | 30 секунд | `Config.AckTimeout` |
| PING retry minimum wait | 5 секунд | `Config.ProbeTimeout` |
| Transport send timeout | 5 секунд | `Config.SendTimeout` |
| Coordinator tick | 1 секунда | `Config.TickInterval` |
| Retry base / cap | 5 / 60 секунд | `Config.Retry` |
| Retry multiplier / count | 2 / 5 | `Config.Retry` |
| Jitter | включён | `Config.Retry.Jitter` |
| Watchdog polling | 10 секунд | `-interval` |
| Control API URL | `http://127.0.0.1:8090` | `-coordinator` |
| Supervisor command timeout | 30 секунд | `watchdog.CommandBackend` |
| Watchdog HTTP timeout | 30 секунд | watchdog CLI |
| Control token | обязательный, default отсутствует | `COORD_CONTROL_TOKEN` |

`ProbeTimeout` задаёт минимальное ожидание между recovery probes, а не отдельный
blocking таймер ожидания PONG. `next_update_by` каждого run определяется ACK/heartbeat.
Executor задаёт собственные `UpdateInterval` и `SendTimeout` явно.

## Встраивание daemon runtime

Приложение-хост создаёт `Coordinator` с настроенным transport и Store, затем
передаёт его runtime. Вызов `Run` становится владельцем loop и после остановки
закрывает role registry и Store:

```go
cfg := daemon.DefaultConfig()
cfg.ControlToken = os.Getenv("COORD_CONTROL_TOKEN")
runtime, err := daemon.New(c, cfg)
if err != nil { return err }
return runtime.Run(ctx)
```

По умолчанию control и metrics слушают только loopback (`8090` и `9091`).
`/readyz` доступен на обоих listeners. Для текущего Run coordinator он означает,
что startup tick завершился, transport receive инициализирован и нет Run с
`RecoveryNeeded`; при 503 тело называет `transport_receive_not_initialized` или
`supervisor_recovery_pending`. Это operational readiness существующего Run
runtime, не подтверждение выполнения полного RT-03 для будущей canonical Task
модели. После инициализации transport recovery control API доступен и при 503,
чтобы supervisor мог завершить recovery; это единственный write path в этом
режиме.
При отмене context runtime останавливает
listeners, дожидается coordinator loop и закрывает его durable stores.
`Run` возвращает ошибки shutdown и закрытия stores вместе с основной ошибкой;
ошибка освобождения snapshot/registry не теряется при штатном выходе.
Оба listeners принимают только loopback-адреса. Для удалённого доступа
используйте отдельный TLS proxy с аутентификацией и сетевой политикой. Runtime
остаётся библиотечным host: транспорт и настройка Coordinator принадлежат
приложению.

## Сборка и watchdog

```sh
make build
./bin/coord-watchdog -help
export COORD_CONTROL_TOKEN='replace-with-your-local-control-token'
./bin/coord-watchdog \
  -coordinator http://127.0.0.1:8090 \
  -interval 10s \
  -fence-command /absolute/path/to/fence-writer \
  -spawn-command /absolute/path/to/spawn-session \
  -stop-command /absolute/path/to/stop-session
```

Замените три executable на supervisor вашей установки. Каждый flag принимает
путь к executable, а не строку shell-команды с аргументами. Supervisor executable
может быть собственным wrapper с фиксированной конфигурацией.
Контракты входных аргументов перечислены в README.

Перед запуском должны работать host coordinator/control API и соответствующий
transport bridge. Watchdog сам не устанавливает plugin и не запускает coordinator.

## Control API

Все запросы требуют `Authorization: Bearer ...`. GET /runs возвращает только
STALLED runs с RecoveryNeeded, а не полный список задач.

| Метод и путь | JSON body | Успех |
|---|---|---|
| `GET /runs` | нет | 200, массив кандидатов |
| `POST /claim` | `run`, `fence`, `claim` | 204 |
| `POST /release` | `run`, `fence`, `claim` | 204 |
| `POST /transfer` | `run`, `fence`, `claim`, `owner` | 204 |
| `POST /fail` | `run`, `fence`, `claim`, `reason` | 204 |

Ошибки: 401 — token не совпал; 400 — неверный JSON; 409 — операция отклонена,
в том числе при stale fence/claim; 404 — неизвестный путь; 405 — неверный метод.

```sh
# Прочитать кандидатов на recovery после запуска host-приложения.
curl --fail-with-body \
  -H "Authorization: Bearer ${COORD_CONTROL_TOKEN}" \
  http://127.0.0.1:8090/runs

# Прочитать Prometheus endpoint после запуска metrics listener.
curl --fail http://127.0.0.1:9091/
```

Пример POST для существующего stalled run с fence=1:

```sh
curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${COORD_CONTROL_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"run":"CF-009-run-1","fence":1,"claim":"manual-recovery-1"}' \
  http://127.0.0.1:8090/claim
```

Claim получает конкретный watchdog/оператор. Transfer разрешён только после
подтверждённого fencing и проверки worktree. Это API для trusted supervisor;
сервер проверяет claim/fence, но не может самостоятельно доказать остановку процесса.
Manual claim не освобождается по времени. Перед ручным release убедитесь,
что процесс, получивший claim, больше не может продолжить recovery.

## Snapshot, backup и restart

Canonical runs, roles и executor checkpoint размещайте в отдельных файлах,
например `state/runs.json`, `state/roles.json`, `state/executor-agent-1.json`.
Не включайте runtime state или token в commits: `state/` и `.env*` игнорируются.

Для согласованной резервной копии остановите host и writers штатно, дождитесь
закрытия Store и скопируйте state directory. Восстановление выполняйте до старта
host из копии вместе с нужным worktree и артефактами. Живой writer нельзя
подменять старым checkpoint; сначала supervisor подтверждает его остановку.

При штатном рестарте coordinator загружает snapshot и повторяет outbox.
Executor использует собственный checkpoint. После recovery он получает TASK
с новым owner/fence и отвечает ACK; старый owner отклоняется.

## Диагностика

| Симптом | Проверка и действие |
|---|---|
| `snapshot already owned` | Найти coordinator, удерживающий flock; остановить его штатно, не запускать второй writer |
| `transport closed` | Восстановить transport и создать новый Adapter/loop; Receive одноразовый |
| TASK доставлен, ACK нет | Проверить executor wrapper и application protocol; delivery не означает acceptance |
| PONG есть, run STALLED | Проверить генерацию heartbeat, next_update_by и task correlation |
| `stale role revision` | Получить текущую revision и подготовить update повторно с новым message_id |
| Role/profile mismatch | Проверить registration, availability роли и все требуемые capabilities |
| Auditor conflict | Назначить отдельный agent_id; новый session address прежнего агента не решает конфликт |
| Claim остался после crash | Подтвердить остановку watchdog/старого writer, затем выполнить контролируемый release |
| Worktree verification failed | Сохранить состояние для анализа; supervisor помечает run FAILED и не запускает замену |

Runtime alerts находятся в `prometheus-alerts.yml`. Их нужно подключить
к вашей конфигурации Prometheus; наличие файла само по себе не включает алерты.

## Неудачная остановка replacement

Ошибка transfer не доказывает, что передача владения не состоялась: ответ мог
потеряться после durable commit. Команда stop обязана подтвердить завершение
replacement и освобождение writer lock даже после принятия TASK. Если stop
завершился ошибкой, watchdog не освобождает claim старого поколения. Перед ручным
release проверьте канонические owner/fence и остановку всех затронутых writers.
Если transfer уже был сохранён, новое поколение и его supervision остаются
каноническими; release старого fence не отменяет состоявшийся transfer.

## Подтверждение результата и registry ownership

Executor передаёт RESULT_ACK в Receipt только после проверки identity,
coordinator sender, correlation и outcome. Получение transport delivery само
по себе не очищает RESULT из outbox. Coordinator повторяет недоставленные
receipts и у терминальных runs; после reconnect executor вызывает Replay.

Registry.New удерживает process lock файла roles до Registry.Close; закрывайте
registry после остановки coordinator. Не открывайте второй registry writer для
чтения revision: используйте существующий owner или остановите его перед reload.
На Linux, macOS, DragonFly BSD, FreeBSD, NetBSD, OpenBSD и illumos используется
kernel flock. На остальных платформах registry и checkpoint store возвращают
ошибку и не открывают состояние без эксклюзивного process lock.

При failed spawn с неизвестным адресом replacement watchdog удерживает claim
до ручного подтверждения cleanup. Известный partial replacement останавливается
перед release. Cancellation основного context не отменяет ограниченный cleanup
context для stop/release; ошибка одного recovery не пропускает остальные runs.

После ErrPersistence Store и Registry прекращают дальнейшие записи, а Registry
также прекращает новые Select. Причина: ошибка могла произойти после rename,
и память уже не доказывает состояние диска. Остановите host, закройте владельцев
и повторно откройте snapshots; не продолжайте запись из старой памяти.
