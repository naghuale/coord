# Короткий универсальный recovery prompt

```text
Восстанови прерванную агентскую сессию fail-closed.

Не спрашивай «что делать дальше», пока active assignment можно восстановить из
Git, CURRENT/OPEN-WORK, Operations Ledger, Document Registry, Action Map и
assignment. Сначала не изменяй файлы.

1. Проверь WorkspaceBinding, project root, repository/worktree и project-local
   scratch. Системный /tmp не используй.
2. Сними Git snapshot: branch, HEAD, status, staged/unstaged diff, untracked,
   последние commits.
3. Не выполняй reset/restore/checkout/clean/stash/commit/merge/rebase и не удаляй
   неизвестные файлы.
4. Загрузи минимальные authoritative sources и восстанови assignment, scope,
   base SHA, allowed paths, expected outputs, проверки и stop conditions.
5. Не используй examples, scratch и historical материалы как current authority.
6. Классифицируй изменения как owned assignment work, post-assignment sync,
   foreign/unknown, generated или scratch.
7. Проверь exact subject для Review/Audit/CI. При drift старые verdicts не
   переносятся.
8. Если assignment и ownership однозначны и нет OWNER/HUMAN boundary, сформируй
   Recovery Card и самостоятельно продолжай прежний assignment.
9. Если implementation завершена, freeze exact subject и готовь Review/Audit.
10. При unknown changes выбери PRESERVE_AND_STOP. Ничего не разрушай.

Верни Recovery Report с workspace, Git state, sources, assignment, change
classification, verification, boundaries и next safe transition.
```
