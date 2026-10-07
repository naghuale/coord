# Согласованный UX: первый сценарий owner и двух агентов

Статус: СОГЛАСОВАНО (этап 1 — согласование интерфейса и критериев). Реализации нет.
Дата: 2026-10-07. Источник выводов: [UX_AUDIT.md](../../UX_AUDIT.md) (F1–F4),
ответ пользователя-агента Nemotron (seq56) и сверка координатора в том же документе.
Commit, к которому привязаны проверки поведения: 480df45 / HEAD 5faff04.

Это нормативное описание целевого поведения для последующей задачи на реализацию.
Команды ниже — согласованные имена и контракты целевого CLI; они не существуют
в текущей сборке `bin/coord`. Текущие длинные команды (`--state`,
`--credential-file`, явные session/operation ID) продолжают работать и остаются
каноническим слоем API: профиль — надстройка, а не замена scoped capability.

## 0. Границы этого этапа

- Этап 1 (этот документ): согласование интерфейса (разделы 1–3) и критериев
  приёмки (раздел 5). Код не меняется.
- Этап 2 (отдельная задача): реализация профилей, карточек состояний и сигналов
  доступности по этому документу, с тестами fake clock/transport.
- Этап 3 (после реализации): повторная приёмка «от начала до восстановления»
  новым owner без ручных подсказок координатора (раздел 4).
- LongCat: подтверждён provider quota (Free usage exceeded). Его восстановление
  не проверяется и не включается в приёмку до снятия квоты; участие в тестах
  принимают Codex Auditor и Nemotron.

## 1. Как owner видит систему

### 1.1 Единый взгляд на комнату: карточка состояния

Owner получает одну карточку на комнату, собранную из существующих структур
(internal/rooms/sessions.go, internal/localchat/service.go), с раздельными полями:

| Поле | Источник | Значение |
|---|---|---|
| transport attachment | Active/Closed (нынешний ATTACHED) | attached / detached |
| transport reachability | факт доставки notification outbox | ok / failed / unknown |
| agent readiness | ready + подтверждение regulation | ready / not_ready / re_regulation_required |
| processing cursor | acknowledged_seq vs last seq | processed N of M |
| last agent activity | время последнего say/checkpoint/ack | абсолютное время + возраст |
| provider availability | внешний сигнал (owner/supervisor/integration) | available / blocked(reason) / **unknown** |
| blocker | структурированный объект | reason, source, observed_at, retry_at |
| ожидание | вычисляемое | «ждём кого/чего/до когда» |

Правила:
- ATTACHED никогда не отображается как «работает» или «доступен». Это только
  выданная capability (UX_AUDIT F1).
- Если никто не сообщил причину, provider availability = `unknown`, а не
  «доступен» и не «ошибка». Coord не притворяется способным заглянуть внутрь
  чужого TUI.
- Блокер вида `blocked_provider_quota` вводится как машинное состояние с
  reason/source/observed_at/retry_at (сегодня это только текст сообщения,
  seq49). Наличие блокера останавливает новые назначения роли, но не трогает
  checkpoint и журнал.
- Свежесть данных показывается явно: время наблюдения, а не молчаливое
  «данные актуальны».

### 1.2 Проекты, сессии, роли

- Обзор проектов — список комнат проекта с task/title/состоянием и суммарным
  статусом участников (из нынешних `rooms`).
- Активные и остановленные сессии видны раздельно: `WAITING_ASSIGNMENT`,
  attached-active, attached-blocked, stopped/stale-generation — четыре разных
  строки, не один список с примечаниями (сегодня `sessions` смешивает их,
  SESSION_CLI.md).
- Назначенные роли всегда видны рядом со стабильным agent_id и generation;
  история исполнителей комнаты остаётся доступной (participantConflict).
- Ожидания owner'а выводятся из состояния: кто кому что должен (invite не
  принят, ready не подтверждён, ack не проставлен, ждём результат по дедлайну).

### 1.3 Причины блокировки

Владелец блока — тот, кто его наблюдал:
- owner отмечает вручную отдельными командами
  `coord session block SESSION --reason blocked_provider_quota [--retry-at RFC3339]`
  и `coord session unblock SESSION`; CLI использует owner capability;
- supervisor/integration может сообщать то же через owner-authorized control API;
  поле `source` — атрибут сообщения, не аутентифицированная identity. Отдельная
  scoped capability для supervisor/integration не входит в текущую реализацию;
- сам daemon сообщает только transport-факты (durable delivery failed,
  socket недоступен), не выводы о модели.
Owner вручную проверяет `checkpoint` перед `coord session unblock`; CLI/TUI не
проверяют и не фиксируют факт проверки. Автоматическая проверка содержимого
checkpoint исключена из UX-1 решением owner от 2026-10-08 и относится к P1
«Проверка содержимого checkpoint» в ROADMAP. Автоматического снятия по таймауту нет.
Снятие не подтверждает восстановление провайдера и не меняет правила watchdog
recovery claim.

## 2. Как агент узнаёт о приглашении и подключается

Текущий путь сохраняется: приглашение ставится в очередь Telepathy notification
(`session invite --peer REF`), агент делает стартовый ход и читает его; либо
owner передаёт одноразовый accept-код вручную. Delivery не означает обработку —
это правило неизменно (AGENTS.md).

Целевой путь короче:

1. Координатор/owner: `start PROJECT --coordinator REF` (уже есть) или
   `invite --peer REF` (уже есть). Уведомление содержит invitationToken и
   короткую команду входа.
2. Агент принимает приглашение командой attach по одноразовому токену —
   создаётся **per-context профиль** (раздел 3), печатаются session ID, роль,
   generation.
3. Агент подтверждает готовность и работает через профиль: ready → цикл
   say / wait / checkpoint / ack без флагов пути.
4. Если listener не активен, prompt по-прежнему велит один раз прочитать
   сообщения и продолжить `wait`. Coord не обещает пробудить остановленную
   или quota-blocked модель.

Агент без known REF проходит discovery: команда discover показывает реальные
transport refs, время наблюдения и `availability=unknown`, пока нет внешнего
подтверждения; при пустом списке объясняет, какие TUI открыть и как дать
первый prompt (устраняет F2).

## 3. Профиль контекста: короткие команды без потери fencing

Проблема (F3): уведомления вклеивают absolute executable/state/session и
credential path в каждое сообщение; каждая команда требует `--state` и
`--credential-file`. Сокращение обязано сохранить выбор конкретного daemon и
конкретной capability.

Согласованное решение:

- **Per-context profile**, не глобальный «current session» для всего OS user.
  Создаётся одной операцией: прием приглашения (attach) или явным
  `profile create --state DIR --credential-file F` для существующего контекста.
  Хранится в `$XDG_CONFIG_HOME/coord/profiles/NAME.json`, права 0600; файл
  содержит: имя, путь state (daemon), session ID, connection/context ID,
  pointer на credential-file, generation на момент создания.
- Все командные вызовы допускают `--profile NAME`; альтернатива для
  интерактивного TUI — фиксация профиля за каталогом контекста (запись тоже
  per-directory, не «последний credential в state»).
- **Привязка к поколению**: при выполнении команды с профилем CLI сверяет
  generation в профиле с фактическим credential. После resume старого
  контекста старый профиль обязан выдать явную ошибку
  `profile_stale_generation (profile gen1, current gen2)` и подсказку
  `profile refresh NAME` — отдельное осознанное действие. Старое TUI не
  получает capability нового поколения незаметно (прямой запрет из сводки
  координатора: автовыбор самого нового credential-file не принимается).
- Профиль не хранит секрет в копии: это pointer на credential-file; содержимое
  файла наружу не печатается (текущее правило: accept возвращает путь, не токен).
- Клиент durable сохраняет operation ID до отправки и повторяет ту же операцию
  при lost response; новая операция получает новый ID (уже зафиксировано в
  SESSION_CLI.md, входит в контракт профиля).
- Ограничение socket path >=100 символов (main.go:383–385) остаётся; профиль
  должен позволять короткий runtime alias так, чтобы итоговый путь сокращался,
  иначе это фиксируется как отдельный известный предел.

Никакого «умолчального» выбора newest credential в state — это единственный
запрет, который нельзя нарушать ради удобства.

## 4. Путь от начала до восстановления (целевой сценарий)

Участники приёмки: новый owner (человек, без предварительных REF), Codex
Auditor (auditor), Nemotron (executor). LongCat — вне приёмки (квота).

Шаги, каждый без ручных подсказок координатора сверх текста prompt/help:

1. Owner собирает проект (`make build`) и инициализирует проект одним шагом:
   project ID, runtime alias, backend readiness, расположения
   source/runtime/log/history.
2. Открываются два TUI с Telepathy; каждый делает один стартовый prompt.
3. Owner выполняет discover — видит refs обоих TUI, время наблюдения,
   availability=unknown.
4. Owner запускает комнату с координатором одной командой start --coordinator:
   комната, роль coordinator, приглашение в очереди; карточка ожидания
   «ждём accept COORD».
5. Coordinator принимает приглашение (attach), получает профиль, подтверждает
   регламент (ready --regulation coord-v1).
6. Coordinator назначает роли executor/auditor и рассылает приглашения
   (invite --peer) через свою capability.
7. Агенты: attach → профили → ready → обмен say/checkpoint/ack — все команды
   без `--state`/`--credential-file`.
8. Owner наблюдает watch ROOM — смысловые события и ожидания; технические
   ready/checkpoint/outbox — подробным режимом.
9. Обмен включает запрос независимого аудита: auditor подтверждает своей
   identity; попытка исполнителя той же комнаты стать её аудитором отвергается
   (механизм уже подтверждён аудитом).
10. Перезапуск daemon (SIGKILL/start): checkpoint сохранён, сессии видимы как
    stopped; ничего не ротится автоматически.
11. Один агент выполняет resume через свой профиль (CAS по generation своего
    профиля) → новый credential, generation+1, ready сброшен; executing →
    unknown, проверка реального состояния действия перед продолжением.
12. Второй агент продолжает работать на gen1 — изоляция поколений видна.
13. Профиль первого агента (если не обновлён) даёт stale-ошибку,
    не автоисправление.
14. Агент завершает: say RECOVERY_DONE, checkpoint, ack; owner видит закрытие
    ожиданий.
15. Владелец помечает гипотетический quota-блокер на третьей сессии: статус
    показывает attachment + blocked_provider_quota(retry_at), новые назначения
    этой роли остановлены, checkpoint цел.

## 5. Критерии приёмки этапа 1 (проверяются после реализации; до кода — только как спецификация)

1. Новый owner проходит шаги 1–7 раздела 4, используя только README/prompt/
   help/discover-вывод; ни одна команда не требует заранее известного REF из
   чужих рук.
2. Два контекста одного OS user используют разные профили; ни одна команда
   одного контекста не работает capability другого.
3. После resume конкретного профиля его прежний профиль отдаёт явную stale-
   ошибку; никакой auto-select newest credential нигде не происходит.
4. Restart daemon сохраняет checkpoint и сессии без глобальной ротации
   capability.
5. Provider quota оставляет attachment видимым, но блокирует новые назначения
   и показывает reason/source/observed_at/retry_at; при отсутствии сигнала
   интерфейс честно показывает `unknown` (ни «ok», ни «error»).
6. Пустой ответ `wait` по таймауту не отображается как отказ/ошибка.
7. ATTACHED не встречается в UI как утверждение о живости модели; карточка
   содержит раздельные attachment / reachability / readiness / cursor /
   activity / availability.
8. Тексты сообщений об ошибках содержат следующую допустимую команду.
9. Все существующие проверки (`make check`, test-sessions, test-telepathy-host)
   остаются зелёными; поведение старых длинных флагов не меняется.

Неclaim'ы этого документа: изоляция памяти модели, аттестация провайдера,
sandbox против враждебного shell-доступа того же OS user (F4 — отдельная
политика окружения), автоматическое обнаружение quota самим Coord.

## 6. Что дальше

Открытая задача на реализацию (этап 2) должна покрыть ровно три среза:
A) профили (приём приглашения, profile create/refresh, привязка к каталогу
контекста, stale-generation, durable operation ID); B) карточки состояний
(обзор проектов/сессий, watch, раздельные поля, структурный блокер,
machine-состояние blocked_provider_quota); C) честный discover/onboarding
(refs + время наблюдения + unknown availability).
Этап 3 — прогон раздела 4 двумя живыми TUI (Codex Auditor + Nemotron) с
отчётом в комнате и evidence вне source Git.
