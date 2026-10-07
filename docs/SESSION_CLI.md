# Ролевые сессии Coord: команды и эксплуатация

Самостоятельный слой совместной работы AI-агентов, без Crewflow.
Роли: coordinator (Orchestrator), executor, reviewer, auditor.
Owner — администратор, не роль AI. TUI и модель не определяют session_id.

Стандартные ролевые инструкции: [ROLE_PROMPTS.md](ROLE_PROMPTS.md).
Они включены в `coord prompt`; применяется только назначенная роль.

## Запуск

```sh
make build
bin/coord help
bin/coord prompt
bin/coord --state state/team --server none rooms
```

Daemon запускается автоматически. --server none отключает Telepathy;
для уведомлений задайте --server или TELEPATHY_SERVER при первом запуске.
Переключение транспорта: stop и повторный запуск с тем же --state.
Coord не запускает модель автоматически. Папка состояния 0700, socket 0600.

## Owner приглашает координатора

Один начальный шаг owner вместо ручного назначения всех участников:

```sh
bin/coord --state state/team start PROJECT --coordinator COORDINATOR_PEER \
  --agent-id stable-coordinator --operation owner-start-1
```

Команда атомарно создаёт комнату, назначает coordinator и ставит его приглашение
в очередь. Повтор с тем же operation ID не создаёт вторую комнату. Координатор
подтверждает приглашение и сам назначает/приглашает остальных через свою capability.
Owner наблюдает и вмешивается. Coord не запускает чужие модели автоматически.

## Owner: задача, роли и чат

Подставьте UUID из ответов вместо ROOM_ID и SESSION_ID.

```sh
bin/coord --state state/team room create TASK-1 --title "Совместная задача"
bin/coord --state state/team rooms
bin/coord --state state/team sessions
bin/coord --state state/team session assign ROOM_ID --agent-id stable-agent-1 \
  --role executor --instructions "Работать только над TASK-1."
bin/coord --state state/team session invite SESSION_ID --operation invite-1
bin/coord --state state/team watch ROOM_ID
bin/coord --state state/team join ROOM_ID
bin/coord --state state/team say ROOM_ID "Уточнение от owner"
bin/coord --state state/team log ROOM_ID
bin/coord --state state/team close ROOM_ID
```

join — owner-чат; /quit завершает наблюдение. close не означает approval,
merge или успех работы. sessions показывает логические назначения, включая
WAITING_ASSIGNMENT и отключённые сессии; ATTACHED — выданная capability,
не утверждение о живом TUI. peers показывает транспортные сессии.

## Вход A: готовое назначение

Owner передаёт агенту session_id, coord prompt и одноразовую команду accept.
Для автоматической доставки приглашения session invite поддерживает --peer REF
при включённом Telepathy. REF берётся из coord peers.

```sh
bin/coord --state state/team accept ONE_TIME_INVITATION
bin/coord --state state/team session context SESSION_ID --credential-file CREDENTIAL_FILE
bin/coord --state state/team session ready SESSION_ID --credential-file CREDENTIAL_FILE \
  --operation ready-1 --regulation coord-v1 --capabilities "bash,git"
bin/coord --state state/team session say SESSION_ID --credential-file CREDENTIAL_FILE \
  --operation introduction-1 "Роль, регламент и checkpoint проверены; блокеров нет."
```

accept возвращает путь credential-file, а не публичный токен.
Не публикуйте содержимое этого файла, приглашения или owner.key.
При потере ответа безопасен повтор того же приглашения до смены поколения.

## Вход B: регистрация без роли

```sh
bin/coord --state state/team session register --agent-id stable-agent-2 \
  --connection separate-context-2 --operation registration-1
bin/coord --state state/team session registration SESSION_ID --credential-file REGISTRATION_FILE

# Owner либо coordinator назначает ожидание:
bin/coord --state state/team session assign ROOM_ID --registration SESSION_ID \
  --role reviewer --instructions "Проверить критерии задачи."

# Агент после ASSIGNED:
bin/coord --state state/team session resume SESSION_ID --credential-file REGISTRATION_FILE \
  --connection separate-context-2 --operation attach-1
```

До назначения WAITING_ASSIGNMENT не даёт доступа к комнатам.
session_id сохраняется после назначения. Дальше используются context, ready, say.
agent_id при регистрации — заявление участника, а не аттестация; owner утверждает его.

## Координатор и регламент

Для assign/invite coordinator передаёт --credential-file собственной сессии.
Права ограничены назначенной комнатой и явно созданными координатором дочерними
задачами, не всеми комнатами проекта. Управление дочерним назначением не пересылает
его журнал в родительский чат автоматически.

```sh
bin/coord --state state/team session assign ROOM_ID --credential-file COORDINATOR_FILE \
  --agent-id stable-agent-3 --role reviewer --instructions "Проверить результат."
bin/coord --state state/team session regulate COORDINATOR_SESSION \
  --credential-file COORDINATOR_FILE --operation regulation-2 \
  --target EXECUTOR_SESSION --regulation project-v2 --instructions "Обновлённый регламент."
```

Изменение регламента — отдельное событие, снимающее ready.
Агент должен повторно подтвердить новую версию. Это не останавливает процесс ОС.

## Ожидание без listener

```sh
bin/coord --state state/team session wait SESSION_ID --credential-file CREDENTIAL_FILE \
  --after 7 --timeout 30
```

Scoped long-poll возвращает новые события либо пустой ответ по таймауту.
Listener Telepathy не требуется после подключения. Сначала TUI нужен один
стартовый промпт; наличие транспорта само не запускает ход модели. После
подключения агент продолжает wait до закрытия комнаты или явной остановки.

## Подтверждение и checkpoint

```json
{
  "progress": "Подготовлено изменение; ожидается проверка",
  "pending_actions": [{"id": "change-1", "state": "executing"}],
  "artifact_refs": ["src/parser.go"],
  "acknowledged_seq": 7
}
```

```sh
bin/coord --state state/team session checkpoint SESSION_ID --credential-file CREDENTIAL_FILE \
  --operation checkpoint-1 --file checkpoint.json
bin/coord --state state/team session ack SESSION_ID --credential-file CREDENTIAL_FILE \
  --operation ack-1 --seq 7
bin/coord --state state/team session context SESSION_ID --credential-file CREDENTIAL_FILE --after 7
bin/coord --state state/team session resume SESSION_ID --credential-file OLD_CREDENTIAL_FILE \
  --connection restored-context --operation resume-2
```

resume выдаёт новый credential-file и generation, прежнее поколение отвергается.
operation ID сохраняется клиентом до запроса: тот же запрос повторяется с тем же ID,
другие данные с этим ID отвергаются. Для нового подтверждения после resume нужен
новый operation ID.

Checkpoint — структурированный прогресс, не скрытая история модели.
executing после resume становится unknown; исполнитель проверяет реальное
состояние действия, не повторяет его автоматически.
ack подтверждает обработку журнала, не успех внешнего действия.
Чтение и транспортная доставка не продвигают cursor; откат и future cursor запрещены.

Один connection ID / Telepathy ref нельзя привязать к разным логическим сессиям,
даже после закрытия. Один agent_id допускается в разных ролях разных комнат.
Совпадение identity исполнителя и аудитора одной задачи не является независимым аудитом.
Смена агента: owner resume с --agent-id и явным текущим --generation; история
прежних участников остаётся. У агента нет права самостоятельно заменить identity.

## Git-история

```sh
bin/coord --state state/team history status
bin/coord --state state/team history export
bin/coord --state state/team history export --content
git -C state/team/history log --oneline
git -C state/team/history show HEAD:projection.json
```

Оперативный snapshot — источник истины. history — отдельный Git-репозиторий,
единственный writer которого Coord. Автоэкспорт объединяет изменения за цикл.
Durable intent восстанавливает commit/cursor без дубликата после сбоя.

По умолчанию только метаданные. --content явно включает сообщения, инструкции,
checkpoints и ссылки. Capability, ключи, приглашения, transport refs и connection ID
не экспортируются. Секреты внутри произвольного текста автоматически не распознаются.
Переключение политики не удаляет прежние commits.

Локальная работа не требует GitLab/GitHub. Owner отдельно настраивает обычный Git:

```sh
git -C state/team/history remote add origin YOUR_PRIVATE_HISTORY_REPOSITORY_URL
git -C state/team/history push --set-upstream origin main
```

Автоматический push истории и disaster restore snapshot из одной Git-проекции
не реализованы. Source repository Coord и runtime history — разные репозитории.

## Доверие и ограничения

Owner API требует отдельную capability; её автоматически читает owner CLI.
Агентские endpoints требуют capability конкретной сессии и generation.
Отсутствие capability не даёт owner-права.

Граница доверия — один пользователь ОС и приватный Unix socket. Агент с полным
shell-доступом к его файлам может прочитать owner.key: для враждебных агентов
нужны отдельные OS users/sandbox и ограниченные tools. Coord не аттестует модель,
не стирает её контекст и не блокирует произвольные записи во внешние ресурсы.

После перезагрузки запустите Coord и resume. Автозапуск ОС и supervisor внешних
writers не реализованы. Fsync/atomic rename не заменяют резервную копию диска.
Экспериментальная schema v1 отвергается без перезаписи: нужен новый --state
либо отдельная офлайн-миграция. Рабочая schema v2.
Лимиты: 64 комнаты, 16 участников комнаты, 100000 событий комнаты.
Snapshot перезаписывается целиком; архивирование и нагрузочная эксплуатация отдельно.

## Проверки

```sh
make check
git diff --check
make test-sessions
make test-telepathy-host TELEPATHY_SERVER=/absolute/path/to/server.mjs
python3 integration/sessions/real_agent.py --owner-message-file OWNER_MESSAGE_FILE
```

test-sessions — тестовые CLI-клиенты без AI. test-telepathy-host — реальный
изолированный MCP с тестовым subprocess. real_agent.py — opt-in реальная модель,
самостоятельно вызывающая scoped CLI, с owner-сообщением и восстановлением.
Raw evidence/credentials остаются под ignored state; их не следует публиковать.
