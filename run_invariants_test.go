package coord

import (
	"context"
	"coord/envelope"
	"coord/fsm"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// checkRunInvariants is the DP-002 mechanism: a transition may clear state it
// meant to keep, or keep state it meant to clear, and prose review misses both.
//
// Подтверждено по коду на момент написания теста:
//   - Terminal ⇒ RecoveryClaim == "" и !RecoveryNeeded. fsm.go:70 Terminal() это
//     Done или Failed; failDeadline (coordinator.go:468) и recover
//     (coordinator.go:498) обнуляют claim, Apply (fsm.go:135) снимает
//     RecoveryNeeded.
//   - RecoveryClaim != "" ⇒ RecoveryNeeded и Status == Stalled.
//     supervisor.go:18 отвергает claim без RecoveryNeeded и без Stalled.
//   - Stalled ⇒ ResumeStatus != "" и наоборот. fsm.go:75 сохраняет прежний
//     статус перед переходом в Stalled; coordinator.go:139 и :496 чистят
//     ResumeStatus при Assign и recover.
//   - Done ⇒ Result != nil. fsm.go:148 записывает payload результата перед Done.
//   - Seen != nil после Assign. coordinator.go:129 создаёт карту.
//
// Outbox у терминального Run законно может держать RESULT-receipt: coordinator.go
// ставит его в outbox перед flush (coordinator.go:331) и удаляет только после
// успешной отправки, поэтому утверждение «Outbox пуст у Done» было бы неверным
// и намеренно не проверяется.
func checkRunInvariants(r fsm.Run) []string {
	var bad []string
	fail := func(format string, args ...any) { bad = append(bad, fmt.Sprintf(format, args...)) }
	if r.RecoveryClaim != "" {
		if r.Status != fsm.Stalled {
			fail("RecoveryClaim=%q при Status=%s, ожидался STALLED", r.RecoveryClaim, r.Status)
		}
		if !r.RecoveryNeeded {
			fail("RecoveryClaim=%q при RecoveryNeeded=false", r.RecoveryClaim)
		}
	}
	if r.Status == fsm.Stalled && r.ResumeStatus == "" {
		fail("STALLED без ResumeStatus: точка продолжения потеряна")
	}
	if r.Status != fsm.Stalled && r.ResumeStatus != "" {
		fail("Status=%s при непустом ResumeStatus=%s: точка продолжения у состояния, которое её не использует", r.Status, r.ResumeStatus)
	}
	if r.Terminal() {
		if r.RecoveryClaim != "" {
			fail("терминальный Run держит RecoveryClaim=%q", r.RecoveryClaim)
		}
		if r.RecoveryNeeded {
			fail("терминальный Run держит RecoveryNeeded")
		}
	}
	if r.Status == fsm.Done && r.Result == nil {
		fail("DONE без Result: результат не принят")
	}
	return bad
}

// checkRunProgression pins the inter-step rules of AGENTS.md: a redelivery
// never advances Attempt, Fence never decreases, and a terminal Run never
// returns to a non-terminal status.
func checkRunProgression(prev, cur fsm.Run) []string {
	var bad []string
	fail := func(format string, args ...any) { bad = append(bad, fmt.Sprintf(format, args...)) }
	if cur.Attempt != prev.Attempt {
		fail("Attempt изменился %d → %d при повторной доставке", prev.Attempt, cur.Attempt)
	}
	if cur.Fence < prev.Fence {
		fail("Fence убыл %d → %d", prev.Fence, cur.Fence)
	}
	if prev.Terminal() && !cur.Terminal() {
		fail("терминальный %s вернулся в %s", prev.Status, cur.Status)
	}
	return bad
}

func assertRunInvariants(t *testing.T, f fixture, step string) fsm.Run {
	t.Helper()
	r, ok := f.s.Get("run")
	if !ok {
		t.Fatalf("%s: run отсутствует", step)
	}
	if bad := checkRunInvariants(r); len(bad) > 0 {
		t.Errorf("%s (status %s): %s", step, r.Status, strings.Join(bad, "; "))
	}
	return r
}

// TestRunInvariantsHoldThroughLifecycle walks a run through every transition
// the coordinator implements and checks the invariants after each step. The
// scenario must actually reach Stalled, recovery with a Fence bump, Done and
// Failed, otherwise the checks below would pass on a shorter path.
func TestRunInvariantsHoldThroughLifecycle(t *testing.T) {
	f := setup(t)
	f.assign(t)
	assertRunInvariants(t, f, "Assign")

	f.handle(t, f.ack())
	prev := assertRunInvariants(t, f, "ACK")

	// Повторная доставка того же ACK: Attempt не растёт.
	repeat := f.ack()
	f.handle(t, repeat)
	after := assertRunInvariants(t, f, "повторный ACK")
	if bad := checkRunProgression(prev, after); len(bad) > 0 {
		t.Errorf("повторный ACK: %s", strings.Join(bad, "; "))
	}

	f.handle(t, f.heartbeat("STARTED"))
	prev = assertRunInvariants(t, f, "PROGRESS")

	// Пропуск heartbeat: Tick переводит Run в STALLED, сохраняя точку
	// продолжения, а исчерпание ProbeTimeout и retry поднимает RecoveryNeeded.
	// Шаги времени те же, что в TestRecoveryCASAndOldOwnerRejected: первый шаг
	// переводит Run в STALLED и ставит probe, следующие два выбирают его.
	for _, d := range []time.Duration{61 * time.Second, time.Second, time.Second} {
		*f.now = f.now.Add(d)
		if err := f.c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	stalled := assertRunInvariants(t, f, "STALLED после пропуска heartbeat")
	if stalled.Status != fsm.Stalled {
		t.Fatalf("ожидался STALLED, получено %s", stalled.Status)
	}
	if !stalled.RecoveryNeeded {
		t.Fatal("сценарий не дошёл до состояния, где требуется recovery")
	}
	if bad := checkRunProgression(prev, stalled); len(bad) > 0 {
		t.Errorf("переход в STALLED: %s", strings.Join(bad, "; "))
	}

	// Recovery: claim, затем recover с повышением Fence.
	if err := f.c.ClaimRecovery("run", stalled.Fence, "watchdog-1"); err != nil {
		t.Fatal(err)
	}
	claimed := assertRunInvariants(t, f, "claim")
	if claimed.RecoveryClaim == "" {
		t.Fatal("claim не записан")
	}
	if err := f.c.RecoverClaimed(context.Background(), "run", "exec-2", claimed.Fence, "watchdog-1"); err != nil {
		t.Fatal(err)
	}
	recovered := assertRunInvariants(t, f, "recover")
	if recovered.Fence <= claimed.Fence {
		t.Fatalf("recover не повысил Fence: %d → %d", claimed.Fence, recovered.Fence)
	}
	if bad := checkRunProgression(claimed, recovered); len(bad) > 0 {
		t.Errorf("recover: %s", strings.Join(bad, "; "))
	}

	// Новый writer подтверждает приём и доводит работу до результата.
	// reply/ack/heartbeat строятся из текущего snapshot, поэтому после recover
	// они несут нового OwnerSession, новый Fence и свежий InReplyTo.
	f.handle(t, f.ack())
	assertRunInvariants(t, f, "ACK нового writer")

	ready := f.heartbeat("RESULT_READY")
	f.handle(t, ready)
	assertRunInvariants(t, f, "RESULT_READY")

	result := f.reply(envelope.Result)
	result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "готово"}
	f.handle(t, result)
	done := assertRunInvariants(t, f, "DONE")
	if done.Status != fsm.Done {
		t.Fatalf("ожидался DONE, получено %s", done.Status)
	}

	// Повторная доставка результата с тем же MessageID: координатор отвечает
	// повторным receipt, а не применяет переход второй раз.
	if err := f.c.Handle(context.Background(), result); err != nil {
		t.Fatalf("повторный RESULT с тем же MessageID отклонён: %v", err)
	}
	afterDone := assertRunInvariants(t, f, "повторный RESULT")
	if bad := checkRunProgression(done, afterDone); len(bad) > 0 {
		t.Errorf("повторный RESULT: %s", strings.Join(bad, "; "))
	}
	if afterDone.Status != fsm.Done {
		t.Fatalf("повторный RESULT изменил статус на %s", afterDone.Status)
	}
	// Тот же результат под новым MessageID — это другой факт, и терминальный
	// Run обязан его отвергнуть, а не тихо принять.
	fresh := result
	fresh.MessageID = envelope.NewID()
	if err := f.c.Handle(context.Background(), fresh); err == nil {
		t.Fatal("терминальный Run принял RESULT под новым MessageID")
	}
	if again := assertRunInvariants(t, f, "RESULT под новым MessageID"); again.Status != fsm.Done {
		t.Fatalf("отказ изменил статус на %s", again.Status)
	}
}

// TestTerminalRunClearsRecoveryState covers the other terminal path: an
// absolute deadline must leave no recovery state behind. The run is claimed
// first on purpose — a terminal transition that does not clear the claim would
// leave a live claim owned by a watchdog on a dead run.
func TestTerminalRunClearsRecoveryState(t *testing.T) {
	f := setup(t)
	f.assign(t)
	assertRunInvariants(t, f, "Assign")

	for _, d := range []time.Duration{31 * time.Second, time.Second, time.Second} {
		*f.now = f.now.Add(d)
		if err := f.c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	stalled, _ := f.s.Get("run")
	if !stalled.RecoveryNeeded {
		t.Fatalf("сценарий не дошёл до RecoveryNeeded: %+v", stalled)
	}
	if err := f.c.ClaimRecovery("run", stalled.Fence, "watchdog-1"); err != nil {
		t.Fatal(err)
	}
	claimed := assertRunInvariants(t, f, "claim")
	if claimed.RecoveryClaim == "" {
		t.Fatal("claim не записан: сценарий не проверяет его снятие")
	}

	// Handle выводит Run в FAILED по абсолютному дедлайну и обязан снять claim.
	*f.now = f.now.Add(24 * time.Hour)
	if err := f.c.Handle(context.Background(), f.ack()); !errors.Is(err, ErrDeadlineExceeded) {
		t.Fatalf("ожидался ErrDeadlineExceeded, получено %v", err)
	}
	failed := assertRunInvariants(t, f, "FAILED по дедлайну")
	if failed.Status != fsm.Failed {
		t.Fatalf("ожидался FAILED, получено %s", failed.Status)
	}
	if failed.RecoveryClaim != "" || failed.RecoveryNeeded {
		t.Fatalf("терминальный Run сохранил recovery state: %+v", failed)
	}
}

// TestStalledExitClearsResumeStatus covers the two transitions where a run
// leaves STALLED without passing through failDeadline: a late heartbeat from the
// same writer, and a RESULT arriving after the readiness probe was missed.
// ResumeStatus must not survive either exit, otherwise the run advertises a
// continuation point that no longer has a STALLED to resume from.
func TestStalledExitClearsResumeStatus(t *testing.T) {
	t.Run("поздний heartbeat возвращает в работу", func(t *testing.T) {
		f := setup(t)
		f.assign(t)
		f.handle(t, f.ack())
		f.handle(t, f.heartbeat("STARTED"))
		stall := func() fsm.Run {
			t.Helper()
			*f.now = f.now.Add(61 * time.Second)
			if err := f.c.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			r, _ := f.s.Get("run")
			return r
		}
		if got := stall(); got.Status != fsm.Stalled {
			t.Fatalf("ожидался STALLED, получено %s", got.Status)
		}
		assertRunInvariants(t, f, "STALLED")
		// Поздний heartbeat того же writer: Run снова в работе.
		f.handle(t, f.heartbeat("STARTED"))
		back := assertRunInvariants(t, f, "выход из STALLED по heartbeat")
		if back.Status == fsm.Stalled {
			t.Fatal("поздний heartbeat не вернул Run в работу")
		}
		if back.ResumeStatus != "" {
			t.Fatalf("выход из STALLED оставил ResumeStatus=%q", back.ResumeStatus)
		}
	})

	t.Run("RESULT после пропущенного probe", func(t *testing.T) {
		f := setup(t)
		f.assign(t)
		f.handle(t, f.ack())
		f.handle(t, f.heartbeat("RESULT_READY"))
		*f.now = f.now.Add(61 * time.Second)
		if err := f.c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		stalled, _ := f.s.Get("run")
		if stalled.Status != fsm.Stalled {
			t.Fatalf("ожидался STALLED, получено %s", stalled.Status)
		}
		if stalled.ResumeStatus != fsm.ResultReady {
			t.Fatalf("STALLED должен помнить RESULT_READY, получено %s", stalled.ResumeStatus)
		}
		assertRunInvariants(t, f, "STALLED")
		result := f.reply(envelope.Result)
		result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "ok"}
		if err := f.c.Handle(context.Background(), result); err != nil {
			t.Fatalf("RESULT из STALLED отвергнут: %v", err)
		}
		done := assertRunInvariants(t, f, "выход из STALLED в DONE")
		if done.Status != fsm.Done {
			t.Fatalf("ожидался DONE, получено %s", done.Status)
		}
		if done.ResumeStatus != "" {
			t.Fatalf("терминальный Run сохранил ResumeStatus=%q", done.ResumeStatus)
		}
	})
}
