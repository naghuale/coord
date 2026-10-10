# Регламент синхронизации архитектурной документации

**Статус:** Proposed  
**Область:** `docs/ARCHITECTURE.md`, `docs/TARGET-ARCHITECTURE.md`, `docs/architecture/COMPONENT-STATUS.yaml`  
**Класс:** процесс разработки репозитория  

## 1. Назначение документов

- `docs/ARCHITECTURE.md` описывает фактически существующий код и разделяет legacy runtime, реализованный canonical core и подключённые production paths.
- `docs/TARGET-ARCHITECTURE.md` показывает путь к Accepted ADR/TASK-MODEL и статус каждого компонента: `implemented`, `partial`, `missing`, `proposed` или `legacy`.
- `docs/architecture/COMPONENT-STATUS.yaml` является единым машиночитаемым реестром статусов, доказательств и недостающих частей компонентов. Он не создаёт норму и не заменяет ADR.
- Accepted ADR и `docs/TASK-MODEL.md` задают нормы.
- Код и тесты доказывают реализацию нормы, но не заменяют архитектурную документацию.

## 2. Словарь статусов

- `implemented`: вся заявленная гарантия реализована, подключена к соответствующему рабочему контуру и подкреплена тестом.
- `partial`: часть гарантии или canonical core реализованы, но production integration либо другая обязательная часть отсутствует.
- `missing`: реализация отсутствует.
- `proposed`: существует только Proposed ADR/RFC/design; это не действующая capability.
- `legacy`: реализация существует, но не является целевой canonical моделью.

Формулировки `готово`, `завершено`, `работает` без уровня зрелости запрещены.

## 3. Пошаговый процесс

### Шаг 1. Определить architecture impact до начала реализации

В assignment указать:

```yaml
architectureImpact:
  affectedComponents: []
  currentArchitecture:
    updateRequired: false
  targetArchitecture:
    updateRequired: false
  componentRegistry:
    updateRequired: false
  reason: ""
```

Если затронуты `store/**`, `task/**`, `agentsession/**`, `transport/**`, `cmd/coordd/**`, `checkpoint/**` или `internal/taskservice/**`, отсутствие архитектурного влияния требует проверяемого основания.

### Шаг 2. Заморозить нормативную основу

Для каждого затронутого компонента перечислить Accepted ADR, TASK-MODEL и действующие решения OWNER. Proposed ADR помечаются как proposed и не используются как доказательство текущей capability.

### Шаг 3. Реализовать код и доказательства

Executor изменяет код и добавляет тест, который падает при поломке заявленной гарантии. Для `TD R2` обязательны Reviewer, независимый Auditor, отрицательные тесты и мутации.

### Шаг 4. Обновить реестр компонентов

В `COMPONENT-STATUS.yaml` обновить:

- статус;
- реализованные гарантии;
- отсутствующие части;
- нормативные источники;
- символы и тесты;
- связанные Issues;
- anchors обоих архитектурных документов.

### Шаг 5. Обновить текущую архитектуру

`ARCHITECTURE.md` должен отражать только подтверждённые свойства текущего кода. Test-only canonical boundary явно отделяется от production caller path. Legacy Run FSM не называется Task workflow.

### Шаг 6. Обновить целевую архитектуру

`TARGET-ARCHITECTURE.md` должен использовать статус из реестра и отдельно перечислять реализованное и отсутствующее. Новый canonical core обычно переводит `missing` в `partial`; только полное подключение гарантии переводит `partial` в `implemented`.

### Шаг 7. Запустить локальный контроль

```bash
python3 scripts/check-architecture-sync
python3 scripts/check-architecture-sync-test
make check
git diff --check
git diff --cached --check
```

### Шаг 8. Независимое ревью

Reviewer сверяет цепочку:

```text
Accepted norm -> code symbol -> failing regression test -> component registry -> ARCHITECTURE -> TARGET-ARCHITECTURE
```

Проверяются фактическая точность, отсутствие повышения зрелости, отделение legacy/canonical/production и существование всех evidence references.

### Шаг 9. CI на точной версии

CI запускает `check-architecture-sync`. Если изменены архитектурно значимые пути, должен измениться реестр компонентов либо существовать явный impact manifest со статусом `N/A` и причиной.

### Шаг 10. После интеграции

Координатор повторяет проверку на integration HEAD, обновляет Operations Ledger, публикует Completion Report и выполняет compaction контекста.

## 4. Автоматические блокировки

- `ARCHITECTURE_STATUS_MISSING`
- `ARCHITECTURE_STATUS_CONFLICT`
- `ARCHITECTURE_EVIDENCE_MISSING`
- `ARCHITECTURE_MATURITY_INFLATED`
- `ARCHITECTURE_PROPOSED_AS_IMPLEMENTED`
- `ARCHITECTURE_DOC_UPDATE_REQUIRED`
- `ARCHITECTURE_ANCHOR_MISSING`
- `ARCHITECTURE_TEST_MISSING`
- `ARCHITECTURE_SYMBOL_MISSING`

## 5. Правила применимости

Документы можно не менять только при явном основании `N/A`, например при внутреннем рефакторинге без изменения компонентов, гарантий, production integration и operational behavior. `N/A` без причины запрещён.

## 6. Условия завершения задачи

Задача, меняющая архитектурный статус, считается готовой только когда:

- реестр валиден;
- оба документа согласованы с реестром;
- evidence существует;
- независимое ревью принято;
- CI зелёный;
- maturity не повышена сверх доказанного уровня;
- Completion Report опубликован.
