# Telepathy 0.8.0: воспроизводимые эксперименты

Harness в `integration/telepathy` использует настоящее ядро coord и установленный
Telepathy MCP server через stdio. Это test-only код; production transport adapter,
bridge для произвольного legacy-агента или supervisor этим не объявляются готовыми.

## Запуск

Нужны macOS/Linux, Go, Python 3 с sqlite3, Node и Telepathy **0.8.0**.
Harness проверяет имя/версию сервера в MCP initialize. Укажите реальный server.mjs:

```sh
export TELEPATHY_SERVER=/absolute/path/to/telepathy/0.8.0/dist/server.mjs
go build -o bin/telepathy-experiment ./integration/telepathy
python3 integration/telepathy/run.py
python3 integration/telepathy/run_checks.py
python3 integration/telepathy/run_subprocess.py
```

Также доступен `make test-telepathy`. Он не входит в обычный `make check`, потому
что требует установленный внешний plugin и запускает процессы с SIGKILL.
Каждый запуск создаёт собственный TELEPATHY_HOME внутри `state/telepathy-experiments/`.
Файлы runtime, checkpoint, SQLite DB и отчёты игнорируются Git. Существующие
пользовательские сессии и обычный Telepathy state не используются.

Участники программные, без модели. Для совместимости с реестром Telepathy они
используют поддерживаемую метку `opencode`; это не запуск OpenCode frontend.
Живые test anchors сохраняют транспортную identity там, где эксперимент
проверяет рестарт процесса с тем же адресом. Новый адрес coordinator после crash
и смена frontend/session identity требуют отдельного routing/recovery контракта.

## Проверки

| Сценарий | Действие | Проверяемый результат |
|---|---|---|
| Round-trip | TASK/read → application ACK → RESULT_READY → RESULT | Snapshot DONE |
| ACK crash window | SIGKILL получателя после durable TASK checkpoint, до ACK | Reload + retry сохраняют attempt/fence/owner |
| Duplicate | Exact-body replay и семантический TASK с новым message_id | Telepathy отклоняет точный текст; тестовая работа выполнена один раз |
| P0: смена owner | A удерживает flock; SIGKILL группы, wait, проверка освобождения; CAS transfer B | Fence 1→2, attempt=3; B получает writer lock после fencing |
| P0: поздняя старая сессия | Новый test messenger воспроизводит HEARTBEAT старого A с fence=1 | Отклонение, ABORT, stale_fence_rejections=1; B завершает run |
| P1: coordinator restart | SIGKILL при STARTED/42%; heartbeat 43% во время outage; restart | Owner/fence=7/attempt/SHA/FSM восстановлены; queued heartbeat обработан; DONE |
| P2: реальный subprocess | Python worker commit 2 из 4 units, SIGKILL на 50%, checkpoint reload, resume | Исходные units сохранены, run DONE |
| P3: side effects | Продолжение worker и повтор всего завершённого input | 4 уникальных units, одна logical completion, повтор не меняет данные |

P0 не изображает «воскрешение» убитого PID. Оригинальный writer подтверждённо
остановлен; поздние сообщения имитирует новый scripted процесс старой сессии,
который не пишет в ресурс и завершает работу после ABORT.

В P0 проверяется `coord_stale_fence_rejections_total=1`: поздний HEARTBEAT
отклонён как старое поколение. Метрики сохраняются в evidence вместе с логами.
Проверка будущего fence относится к unit tests, а не к этому live сценарию.
В P1 counters нового процесса начинаются заново; восстановление подтверждается
snapshot и событиями, а не непрерывностью counters через рестарт.

## Что означает fencing

Fence не является секретом или доказательством остановки процесса. Coordinator
принимает только точное совпадение текущего fence и canonical owner; будущий
fence тоже отклоняется. Смена owner/fence публикуется после durable commit.

В P0 отсутствие двух активных writers проверяется реальным advisory flock:
пока A жив, попытка второго lock не проходит; после SIGKILL lock доступен;
B владеет им после передачи run. Это гарантия для сотрудничающих test writers.
Процесс, игнорирующий advisory lock, таким тестом не защищён. Для внешнего ресурса
нужна проверка поколения в самом ресурсе либо подтверждённая остановка старого writer.

## Контракт subprocess

`worker.py` реализует RESUME/IDEMPOTENT для своих SQLite side effects. Строка
данных и её deduplication key создаются в одной транзакции с unique primary key.
Progress file может отстать от commit; после crash состояние определяется по DB,
а не по одному progress_pct. Контроллер повторно читает checkpoint с диска после
SIGKILL и передаёт вход с progress=50 в новый subprocess.

Проверка относится к этому worker и его транзакционному ресурсу. Она не доказывает
идемпотентность произвольной модели, файлового writer или обращения к внешнему API.
Такие side effects требуют собственного resume/deduplication контракта.

## Свидетельства и границы

В stdout driver выводит путь evidence. В нём сохраняются report.json, events JSONL,
snapshots, checkpoints, данные subprocess и hashes тестируемых исходников.
Для P0/P1 и P2/P3 также сохраняется diff tracked-файлов относительно base commit.
Неудачный запуск сохраняет failure.json; очистка останавливает только созданные
test process groups. Подробные отчёты не помещаются в Git вместе с runtime state.

Эксперименты подтверждают process-crash recovery для указанных окон. Они не
проверяют power-loss durability, network partition с неподконтрольным writer,
lease enforcement, frontend hooks, receipt/replay результата и production нагрузку.

Реальные LLM-агенты не участвовали: не проверены обработка retry с тем же attempt,
соблюдение resume-контракта, реакция модели на ABORT и недетерминизм повторных запусков.

Production bridge для L0-агентов (legacy-скриптов, CLI-утилит и агентов без SDK)
не реализован и не протестирован. Python worker — контролируемая среда с явным
resume-контрактом; его checkpoint не доказывает восстановление произвольного legacy-кода.

Не проверены потеря сообщения между статусом stored и read_messages, длительные
network partitions и конкурентные read_messages от нескольких читателей.
MCP server перезапускался вместе с тестовыми участниками; отдельный рестарт
Telepathy server при продолжающих работать приложениях и восстановление их
подключений требуют самостоятельного эксперимента.

Добавление одного LeaseExpiresAt в coordinator недостаточно: нужны правила
выдачи/renewal, поведение executor при потере lease и контроль ресурса. В этой
итерации lease не добавляется и heartbeat deadline не объявляется writer lease.
