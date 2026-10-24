# Эксплуатация и команды

## GitHub observation fixtures

`github_observation_fixtures` задаёт только детерминированные in-memory записи. Adapter принимает `OBSERVE_ISSUE`, `OBSERVE_PULL_REQUEST` и `OBSERVE_PIPELINE`; любые GitHub writes отклоняются. Результаты имеют evidence class `FIXTURE_NOT_LIVE` и не подходят для production readiness. Не добавляйте credentials в fixture config. Неизвестный pipeline state остаётся `NOT_EVALUATED`, а legacy Issue/PR без совпадающего marker остаётся наблюдённым, но незавершённым.

Текущая поставка содержит библиотеку coordinator, production executable `coordd`
для текущего Run runtime на versioned canonical snapshot, локальное демо и отдельный watchdog CLI. Есть также
ограниченный самостоятельный Telepathy host/worker
([TELEPATHY_HOST.md](TELEPATHY_HOST.md)). P0-2 вводит typed Task/Event model и
внутреннее создание Task. Схема canonical state v4 добавляет `OwnerCredential`;
`coordd` поддерживает одноразовую bootstrap-церемонию, подписанные команды
регистрации/отзыва ключа и Owner-команду `task.create`. Полный Task workflow,
Run assignment, agent RESULT и cutover остальных writers ещё не подключены.

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
остаётся доступным для встраивания host: транспорт и настройка Coordinator
принадлежат приложению.

## Production Run daemon (`coordd`)

Соберите бинарник и запустите его с единственным аргументом конфигурации:

```sh
make build
./bin/coordd -config /absolute/path/coordd.json
```

Файл конфигурации — JSON schema version 1. Неизвестные поля, отсутствующая или
неподдерживаемая версия, неполный transport, нечитаемые ключи и нелокальные
control/metrics addresses отклоняются до создания state namespace. Файл не
содержит сами секреты: в нём указываются пути к control token и Ed25519 private
key. Защитите эти файлы правами ОС. Пример структуры (значения путей и ключей
замените своими):

```json
{
  "schema_version": 1,
  "state_dir": "/absolute/path/coord-state",
  "control_addr": "127.0.0.1:8090",
  "metrics_addr": "127.0.0.1:9091",
  "control_token_file": "/absolute/path/coord-control.token",
  "transport": {
    "kind": "telepathy",
    "server": "/absolute/path/telepathy/dist/server.mjs",
    "home": "/absolute/path/telepathy-home",
    "directory": "/absolute/path/session-presence",
    "anchor_pid": 12345,
    "session": "opencode-12345",
    "private_key_file": "/absolute/path/coord-private-key.base64",
    "peers": {
      "opencode-67890": "<base64 Ed25519 public key>"
    }
  }
}
```

`node` должен быть доступен в `PATH`; `server` — Telepathy 0.8.0 MCP server.
`anchor_pid` задаёт ожидаемую сессию `opencode-<pid>`; перед запуском Telepathy
проверяет её через `list_peers`. `peers` содержит закреплённые ключи удалённых
сессий. Создайте отдельные живые anchor и не указывайте PID другой работающей
сессии. Транспорт обязателен: отсутствие или ошибка запуска завершают startup;
namespace lock, Store и RoleRegistry закрываются при ошибке.

Перед Store и RoleRegistry daemon получает эксклюзивную `namespace.lock` на весь
каталог `state_dir`, затем compatibility lock `runs.json.lock`, canonical store
lock `canonical.json.lock` и lock RoleRegistry. Второй daemon с тем же каталогом
завершается до listeners. Legacy Run writers блокируются compatibility lock.
Файловые locks компонентов сохраняются как дополнительная защита.
После SIGINT/SIGTERM daemon завершает HTTP handlers и coordinator loop, закрывает
transport process, RoleRegistry и Store, затем освобождает namespace lock.
`/readyz` закрыт до startup tick и инициализации transport receive, а также при
pending supervisor recovery. При активном legacy Run, занятом compatibility
lock или неизвестном canonical schema `coordd` остаётся живым в diagnostic-only
режиме: `/readyz`=503, transport и coordinator loop не запускаются. Обычная
readiness пока не является полной RT-03 проверкой Task workflow.

Exit codes:

- `0` — штатное завершение после SIGINT/SIGTERM.
- `1` — ошибка runtime, transport startup, ownership или graceful shutdown.
- `2` — неверные аргументы или конфигурация; runtime state не открывается.

При ошибке до lock stderr показывает `coordd: not started`, причину, выбранный
или отсутствующий transport, поддерживаемый kind и строки `ownership: not
acquired` / `namespace lock: not acquired`. Ошибка запуска Telepathy после lock
возвращает `1` и сообщает `namespace lock: released` после cleanup.

Runtime config не принимает override-флаги. Для изменения параметров отредактируйте
конфигурационный файл и перезапустите daemon через внешний supervisor.

### Owner credential bootstrap и подписанные команды

До первой Owner-команды зарегистрируйте минимум два публичных COSE ключа при
инициализации canonical state. Файл `credentials.json` содержит только
публичный ключ authenticator, без private material:

```json
[
  {"id":"owner-primary","cose_public_key":"<base64 COSE_Key>"},
  {"id":"owner-backup","cose_public_key":"<base64 COSE_Key>"}
]
```

Запустите bootstrap и сравните напечатанные SHA-256 отпечатки с физическими
authenticators:

```sh
./bin/coordd owner bootstrap --state-dir /absolute/path/coord-state \
  --principal owner-1 --credentials /absolute/path/credentials.json
```

Команда требует точный ввод `BOOTSTRAP owner-1`, блокирует canonical store и
фиксирует обе credentials одним сохранением в schema v4. Bootstrap глобально
одноразовый: после первой записи нельзя добавить новый principal повторной
инициализацией; при потере всех ключей требуется отдельное новое пустое state
directory, старое состояние остаётся архивом. Утверждения публичных отпечатков
при первом запуске — TOFU-риск, который оператор должен сверить вручную.

После bootstrap существующий control listener принимает `POST /owner/challenge`
и `POST /owner/command`; для обеих команд требуется `Authorization: Bearer …`.
Control token только допускает запрос в loopback-канал. Он не подтверждает
Owner и сам по себе не разрешает запись. Challenge связывает domain
`coord.owner-command.v1`, principal, credential, message ID, revision, nonce,
срок и точные байты payload. Caller должен получить WebAuthn assertion
(`webauthn.get`, UP+UV) от зарегистрированного authenticator и передать его
вторым запросом. Daemon проверяет assertion, актуальность credential и payload,
а затем атомарно сохраняет эффект и durable receipt; challenges теряются при
перезапуске daemon.

В JSON API поля Go-типа `[]byte` кодируются base64. Challenge request имеет поля
`kind`, `message_id`, `principal_id`, `credential_id`, `expected_credential_revision`
и `payload` (base64 от точных JSON байтов команды). Ответ — подписываемый command
envelope. Command request содержит `challenge_id`, неизменённый `command` и
`assertion` с base64-полями `client_data_json`, `authenticator_data` и `signature`.
Ответ включает durable `receipt` и `replayed`; точный повтор уже закоммиченной
команды возвращает прежний receipt без дополнительной записи.

Поддержаны только `owner.credential.register`, `owner.credential.revoke` и
`task.create`. Последний создаёт Task с `OwnerID` из проверенного credential;
Task, событие, Inbox receipt и Owner receipt записываются одним commit.
Assignment offer/activation/handoff, delegation и прочие Task transitions
этим endpoint не принимаются. Защита schema v4 не делает новые поля читаемыми
старыми бинарниками: запись старой схемы поверх v4 отклоняется как downgrade.

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
