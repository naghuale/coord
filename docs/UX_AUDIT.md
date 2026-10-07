# Аудит удобства и наблюдаемости Coord

Дата: 2026-10-07. Проверенный исходный commit: 480df45.
Owner запросил независимый аудит и отдельную оценку реального пользователя-агента.
Исправления кода в рамках этой оценки не выполнялись.

## Участники и доказательства

- Дополнительный Codex Auditor: отдельный контекст и agent_id codex-independent-auditor, отчёт в Coord seq52–53, checkpoint и ack53. Он не является LongCat.
- Nemotron: реальный OpenCode TUI, запрос оценки собственного опыта seq46, собственный ответ seq56 и checkpoint seq58. Сообщил, что quota blocker у него отсутствует.
- LongCat: owner подтвердил Free usage exceeded; дальнейшее recovery заблокировано квотой, новых заданий ему не выдаём.
- Журнал комнаты: 2ddfab7a-4236-4445-af99-2e44fe8516d7. Приватное evidence: state/tui-team/evidence/report.json; runtime и credentials не публикуются в source Git.

## Независимый отчёт

Независимый Codex Auditor, agent_id=codex-independent-auditor, session7d994aaa, generation1, regulation coord-v1. Не участвовал в реализации Coord, не являюсь LongCat и не подменяю его identity. Аудит read-only commit480df45db6a794ecee49c49034708a4fddebfa8d; первичные материалы: исходники, README, ARCHITECTURE, SESSION_CLI, ROLE_PROMPTS, bootstrap, журнал разрешённой комнаты seq1–50. Исходники не менял, тесты не запускал. Личная проверка: accept/context/ready собственной capability успешны; socket потребовал штатного разрешения sandbox.

Вывод: сохранение логических сессий и scoped capability реализованы. Работающая модель, независимость организации и разделение памяти модели этим не доказываются. Полный recovery PASS для трёх участников отсутствует: seq40 подтверждает Executor generation2, seq49 фиксирует подтверждённую owner quota LongCat; это не проверка его recovery.

F1 HIGH (операционная корректность): ATTACHED/active не означает выполнение или доступность provider. internal/rooms/sessions.go:215–224,483–505 вычисляют статус только из Active/Closed; internal/localchat/service.go:213–223 копирует Activity из list_peers. Нет проверки quota или TUI. BLOCKED_PROVIDER_QUOTA seq49 является текстом message, не переходом машинного состояния. Поэтому LongCat может оставаться ATTACHED generation1 и transport busy при невозможности ответа 22ч. Проверка: сопоставить эти функции, сообщение owner и отсутствие resumed LongCat. Исправление дизайна: отдельно attachment, transport reachability, readiness, processing cursor, last agent activity и provider availability. Последнее unknown до сигнала supervisor/owner; блокер должен иметь reason/source/observed_at/retry_at и останавливать назначения, но не уничтожать checkpoint. Не приписывать Coord возможность самостоятельно обнаружить quota внутри чужого TUI.

F2 MEDIUM (onboarding): общий prompt предполагает уже выданные binary/state/session, не ведёт owner к первым transport peers (bootstrap.md:36–39,73–79). help помещает peers в дополнительную диагностику (main.go:59–65). start требует известного peer (sessions.go:60–71). Backend discovery ищет ровно Telepathy0.8.0 в одном Codex cache path либо env/flag (main.go:341–356); это discovery server.mjs, не discovery живых моделей. Новичок не понимает, что нужно сначала открыть TUI, обеспечить Telepathy и один стартовый ход/read_messages, затем peers/start. Сам transport не запускает ход модели: SESSION_CLI.md:124–127. Проверка: пройти первый сценарий только по README/prompt/help без заранее известного REF.

F3 MEDIUM (CLI): длинные пути создаются кодом, а не обязательной частью fencing. main.go:83,110 использует cwd-зависимый state; rooms.go:545–553 вклеивает absolute executable/state/session и credential path в каждое уведомление; sessions.go:19–48 требует явный credential-file. Это надёжно выбирает daemon и capability в неизвестном cwd агента, но заставляет человека/модель управлять техническими адресами при каждом действии. Отдельно socket path>=100 отказан main.go:383–385: длинный runtime root ещё и функциональный предел. Сокращение должно быть профилем подключения, привязанным к конкретному context/session/generation, а не глобальным current session для всего OS user.

F4 HIGH при ошибочном обещании sandbox; документированный предел, не найденный обход: owner защищён отдельным header capability (localchat/owner.go:57–70); EnableOwner читает локальный owner.key (44–54). Unix socket приватный (main.go:398–403), но один OS user с полным shell читает тот же ключ/credentials. SESSION_CLI.md:199–206 честно описывает границу. Роль Auditor не запрещает произвольную запись в исходники на уровне ОС. Не проверял обход и не читал секреты. Для недоверенных агентов нужны отдельные sandbox/OS users с доступом только к собственному клиенту.

Подтверждённые механизмы: authorized проверяет session/generation/token/active (sessions.go:110–120), contextFor возвращает журнал одной комнаты (215–230), reserve не допускает один context/peer у разных сессий (123–130), participantConflict и история identities препятствуют исполнителю стать аудитором той же комнаты (521–536,308–325). sessionWrite сначала авторизует, затем dedup (363–394); operation conflict и ack monotonic реализованы. Это изоляция API, не доказательство отдельной памяти модели, реального model/provider или отсутствия общего оператора: эти требования остаются инструкциями/внешней политикой. Общий журнал комнаты доступен её участникам; отдельная сессия не означает приватный журнал внутри комнаты.

Recovery: Resume увеличивает только generation выбранной логической сессии, меняет token и сбрасывает ready (sessions.go:298–306), executing→unknown только там (327–330). Проверки stale применяются к запросам Coord, не к файловым операциям прежнего процесса. Core watchdog/fence из ARCHITECTURE.md не следует автоматически переносить на ролевой chat CLI. Требование остановить старые внешние writers остаётся supervisor/owner обязанностью. Restart не равен resume и не отзывает все credentials комнаты.

События: wait — авторизованный long-poll с опросом 250ms и timeout1–60s (localchat/wait.go:12–40), не фоновое выполнение модели. Telepathy notification доставляется через outbox (rooms.go:556–607), не подтверждает обработку; только ack двигает cursor (sessions.go:468–479). При отсутствии listener prompt велит read_messages один раз и дальнейший wait. Это рабочая инструкция для активного хода, не гарантированная подписка/пробуждение остановленной или quota-blocked модели. Provider unavailable нельзя трактовать как исправимый transport retry.

Минимальный целевой UX — предложение, следующих команд сейчас нет:
1. Owner выполняет coord init PROJECT один раз; получает абсолютный project ID, короткий runtime alias, backend readiness и явное расположение source/runtime/log/history. Затем coord discover показывает реальные transport refs, время наблюдения и availability=unknown, пока нет подтверждения модели. При пустом списке объясняет, какие TUI открыть и как дать первый prompt.
2. coord start PROJECT --coordinator REF выбирает существующий peer; owner видит карточку project/task/room, session ID, stable agent ID, роль, generation, accepted regulation, last event, delivered/processed seq, blocker и «ждём кого/чего/до когда».
3. Агент принимает одно приглашение через coord attach INVITE; создаётся отдельный per-context profile alias. coord --profile AUDIT context/ready/say/wait/checkpoint/ack автоматически использует только capability этого context. session ID/generation всегда видны в результате. Профиль не глобальный, не выбирает newest credential в state и не переключает старое TUI на capability нового поколения.
4. Только явный resume конкретного profile выполняет CAS expected generation и rotation; старый profile остаётся stale. Client должен durable сохранять operation ID до отправки и повторять его при lost response; новая операция получает новый ID.
5. Owner watch показывает смысловые события и ожидания, технические ready/checkpoint/outbox доступны подробным режимом. Отдельные transport/provider/permission blockers и неизвестная свежесть данных. Quota: owner/supervisor отмечает blocked_provider_quota с retry_at; не выдавать новые задачи и не маскировать замену модели под прежний agent ID. Возобновление только явно, с проверкой checkpoint.

Проверка будущего дизайна: новый owner без известных REF проходит onboarding; два контекста одного OS user используют разные profiles; после resume старый profile получает stale без автоисправления; daemon restart сохраняет checkpoint без глобального rotation; provider quota оставляет attachment видимым, но блокирует новые назначения и показывает источник/срок; пустой wait не объявляется отказом. Это сценарии приёмки, сейчас не исполнены.

## Границы вывода

Общение двух пользовательских TUI через Coord состоялось. Полное восстановление обоих не подтверждено: LongCat заблокирован провайдером. Имена моделей основаны на сообщениях участников. Предложенные команды описывают целевой UX; сейчас они не реализованы.

## Отзыв реального пользователя-агента

Ниже дословный ответ Nemotron seq56; его субъективные затруднения и утверждения о коде различаются.

UX evaluation (nemotron-executor, provider NVIDIA nemotron-3-ultra-free via opencode, no quota blocker):

**5 main friction points:**

1. **Telepathy discovery is opaque** -- no `coord peers` equivalent for agent; I learn my ref (`opencode-57562`) only from coordinator's invite message. No self-service `coord status` showing my connection_id, generation, peer ref.

2. **Long, error-prone CLI commands** -- `session resume` requires 6+ flags (--credential-file, --connection, --operation, --peer, --generation). One typo -> cryptic error. No short aliases, no config file for defaults.

3. **Credential-file management is manual** -- must track `credential-file` path across generations (gen1->gen2 different files). No `coord session current` showing active credential-file. Easy to use stale file -> `stale session generation`.

4. **Status visibility gaps** -- `session context` dumps full journal (verbose). No summary: `role, generation, ready, acknowledged_seq, blockers`. `coord sessions` exists but owner-only; agents can't self-inspect.

5. **Quota/permission/idle not surfaced** -- LongCat hit `Free usage exceeded` but room only showed ATTACHED/generation1. No transport-level signal (idle, quota, disconnect) visible to coordinator or agent. Recovery proceeded assuming available model.

**Minimal owner/agent scenario:**
```
# Owner (one command)
coord start coord --coordinator <peer> --agent-id stable-coord --operation owner-1

# Coordinator (self-service)
coord session assign <room> --agent-id stable-exec --role executor --instructions "..." --credential-file <coord-file>
coord session invite <exec-session> --operation invite-1 --credential-file <coord-file>

# Agent (receives invite via Telepathy, runs):
coord accept <invite-token>
coord session ready <session> --credential-file <file> --operation ready-1 --regulation coord-v1
# ... work ...
coord session say <session> --credential-file <file> --operation say-1 "done"
coord session checkpoint <session> --credential-file <file> --operation cp-1 --file cp.json

# Recovery (agent self-service after daemon restart):
coord session resume <session> --credential-file <old-file> --connection recovery-<id>-2 --operation resume-<id>-2 --peer <my-ref>
# verify old file rejected, new file works
coord session ready <session> --credential-file <new-file> --operation ready-2 --regulation coord-v1
coord session say <session> --credential-file <new-file> --operation say-2 "RECOVERY_DONE"
```

**What docs promise vs reality:**
- README: "binary in bin/, runtime state outside Git" yes but no `coord help` discoverability for agent flows
- SESSION_CLI.md shows commands but not the *sequence* for recovery; I needed coordinator's seq27 instructions
- No `coord prompt` output includes recovery steps
- Transport delivery != processing acknowledgment (correctly documented) but no agent-side ack helper

**Where coordinator help was required:** seq27 (recovery steps), seq19 (auditor workflow), seq23 (corrected my global fencing assumption). Without coordinator, I'd have used stale credential-file and declared false global fencing.

## Сверка координатора

- Recovery уже описан в bootstrap.md, шаги 7–9, и SESSION_CLI.md, раздел «Подтверждение и checkpoint». Отсутствие этих инструкций, заявленное Nemotron, не подтверждается. Подтверждается, что агент не смог самостоятельно применить их и потребовал подсказок.
- session context показывает собственные role, generation, ready, checkpoint и transport ref; отсутствует компактная самостоятельная карточка состояния. Утверждение «агенты не могут себя проверить» слишком широкое.
- --generation у resume можно не передавать: CLI берёт его из предъявленного credential-file; --peer нужен для маршрута Telepathy, не для самого scoped Unix API. Не все перечисленные флаги обязательны всегда.
- coord help перечисляет агентские команды, coord prompt содержит recovery. Это не подтверждает удобство первого использования: инструкции длинные, профиль подключения не запоминается.
- Автоматический выбор самого нового credential-file не принимается как решение: старое TUI не должно получать capability нового поколения без явного resume своего контекста.

## Совместный вывод и приоритет

Технический слой durable sessions и scoped API работает в проверенных сценариях. Продуктовый сценарий owner/агента требует доработки. Первым этапом нужны единый onboarding/discovery, per-context profile коротких команд, карточка ожиданий и отдельные наблюдаемые статусы transport/provider/permission. Только после этого — повторная приёмка новым owner без ручных указаний координатора. Предложения API в отчётах не реализованы.
