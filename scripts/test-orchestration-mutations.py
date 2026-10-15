#!/usr/bin/env python3
"""Development-only mutation harness. Never edits the source checkout.
Run through coord-project-tmp; output and copied mutants remain project-local.
"""
import hashlib, json, os, pathlib, shutil, subprocess, tempfile
ROOT = pathlib.Path(__file__).resolve().parent.parent
scratch = pathlib.Path(os.environ.get('COORD_SESSION_TMP', ''))
project = pathlib.Path(os.environ.get('COORD_PROJECT_ROOT', ''))
if not scratch.is_absolute() or not scratch.is_relative_to(project / '.scratch' / 'tmp'):
    raise SystemExit('run through scripts/coord-project-tmp')
OUTPUT = pathlib.Path(tempfile.mkdtemp(prefix='orchestration-mutations-', dir=scratch))
# (id, test, assertion, source replacements). Mutants change the production path,
# including both checks where an invariant has defense in depth.
MUTANTS = [
 ('M01','TestExternalCallRequiresDurableDispatchingMarker','external call preceded durable DISPATCHING',[
 ('internal/orchestration/runtime.go','a, err = r.Store.StartAction(id, r.DispatcherID, a.Claim.Epoch, r.Now())','err = nil // MUTATION: external call before durable dispatch-start')]),
 ('M02','TestRuntimeRetryPreservesIdempotencyKey','retry idempotency key changed',[
 ('internal/orchestration/runtime.go','result, err = r.Adapter.Observe(callCtx, a)','a.Key += "/retry"\n result, err = r.Adapter.Observe(callCtx, a)')]),
 ('M03','TestNewObservationInstanceHasDifferentKey','new observation reused cache key',[
 ('internal/externalaction/model.go','1, a.Kind, a.OperationInstanceID, a.Binding, a.RequestDigest','1, a.Kind, "", a.Binding, a.RequestDigest')]),
 ('M04','TestExpiredClaimReconciledBeforeRedispatch','blind launch',[
 ('internal/orchestration/runtime.go','result, err = r.Adapter.Observe(callCtx, a)','result, err = r.Adapter.Execute(callCtx, a)')]),
 ('M05','TestLateClaimResultCannotCompleteNewClaim','late claim accepted',[
 ('store/orchestration.go','a.Claim.Epoch != o.ClaimEpoch || a.Terminal()','false || a.Terminal()'),
 ('internal/externalaction/model.go','o.ClaimEpoch == a.Claim.Epoch','true')]),
 ('M06','TestFenceDeliveryIsNotFenceConfirmation','fence delivery counted as logical fence confirmation',[
 ('store/orchestration.go','if o.Result.Outcome != "FENCED" {','if false && o.Result.Outcome != "FENCED" {'),
 ('internal/externalaction/model.go','p.Kind != Fence || p.Result == nil || p.Result.Outcome != "FENCED"','p.Kind != Fence || p.Result == nil')]),
 ('M07','TestMissingStopProofRejectsReplacement','missing stop proof accepted',[
 ('store/orchestration.go','if !o.Result.Stopped || o.Result.StopMethod != "SUPERVISOR_CONFIRMED_EXIT" || o.Result.Outcome != "STOPPED" {','if false && (!o.Result.Stopped || o.Result.StopMethod != "SUPERVISOR_CONFIRMED_EXIT" || o.Result.Outcome != "STOPPED") {'),
 ('internal/externalaction/model.go','p.Kind != Stop || p.Result == nil || !p.Result.Stopped || p.Result.StopMethod != "SUPERVISOR_CONFIRMED_EXIT"','p.Kind != Stop || p.Result == nil || p.Result.StopMethod != "SUPERVISOR_CONFIRMED_EXIT"')]),
 ('M08','TestRestartDoesNotTrustPersistedRunningState','persisted RUNNING trusted',[
 ('store/orchestration.go','if startup {','if false && startup {')]),
 ('M09','TestProjectionBundleUsesOneRevisionAndRebuilds','mixed projection revisions',[
 ('internal/projection/writer.go','}{s.Revision, s.Orchestration.Actions}','}{s.Revision + 1, s.Orchestration.Actions}')]),
 # No CandidateDecision/OWNER authority exists in this slice. This mutation
 # verifies rejecting the unsupported free-text OWNER result at acceptance;
 # accepted-norm reasoning remains explicitly deferred in the ADR.
 ('M10','TestFreeTextOwnerReasonCannotBecomeAuthority','free-text NEW_NORM_REQUIRED became authority',[
 ('store/orchestration.go','if o.Result.Outcome != "DELIVERED" {','if false && o.Result.Outcome != "DELIVERED" {')]),
 ('M11','TestRetryExhaustionCreatesDurableBlockerAndNoBusyLoop','retry exhaustion not blocked',[
 ('store/orchestration.go','a.Status = externalaction.Blocked','a.Status = externalaction.Ambiguous')]),
 ('M12','TestRetryExhaustionCreatesDurableBlockerAndNoBusyLoop','blocked action busy loop',[
 ('internal/orchestration/runtime.go','if a.Status == externalaction.Observed {','if a.Status == externalaction.Blocked { return r.Store.UpdateCanonical(func(s *store.CanonicalState) error { s.Orchestration.Events = append(s.Orchestration.Events, externalaction.Event{ActionID:a.ID,Revision:s.Revision+1,Digest:externalaction.Digest(a),At:r.Now()}); return nil }) }\n if a.Status == externalaction.Observed {')]),
 ('M13','TestUnstartedClaimReclaimSeparatesEpoch','reclaimed epoch did not advance',[
 ('store/orchestration.go','a.Claim.Epoch++\n\t\ta.Claim.DispatcherID = dispatcher','a.Claim.Epoch += 0\n\t\ta.Claim.DispatcherID = dispatcher')]),
 ('M14','TestRecoveryFencesClaimEpochBeforeLateResult','recovery did not fence old claim epoch',[
 ('store/orchestration.go','a.Status = externalaction.Ambiguous\n\t\t\ta.Claim.Epoch++\n\t\t\ta.Claim.DispatcherID = ""','a.Status = externalaction.Ambiguous\n\t\t\ta.Claim.Epoch += 0\n\t\t\ta.Claim.DispatcherID = ""')]),
]

def run(directory, name, args):
    p = subprocess.run(args, cwd=directory, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    (OUTPUT / (name + '.log')).write_text(p.stdout)
    return p

source = OUTPUT / 'baseline-source'
shutil.copytree(ROOT, source, ignore=shutil.ignore_patterns('.git','.scratch','bin','__pycache__'))
source_files = {str(p.relative_to(source)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(source.rglob('*')) if p.is_file()}
source_digest = hashlib.sha256(json.dumps(source_files,sort_keys=True).encode()).hexdigest()
(OUTPUT / 'source-manifest.json').write_text(json.dumps(source_files,indent=2,sort_keys=True))
baseline = run(source, 'baseline', ['go','test','-count=1','./internal/orchestration'])
if baseline.returncode:
    raise SystemExit('baseline FAIL: ' + baseline.stdout)
results = []
for mid, test, assertion, changes in MUTANTS:
    dest = OUTPUT / mid
    shutil.copytree(source, dest, ignore=shutil.ignore_patterns('.git','.scratch','bin','__pycache__'))
    for path, before, after in changes:
        p = dest / path
        text = p.read_text()
        if text.count(before) != 1:
            raise SystemExit(f'{mid}: source anchor count {text.count(before)} != 1: {before}')
        p.write_text(text.replace(before, after))
    compile_result = run(dest, mid + '-compile', ['go','test','./internal/orchestration','-run','^$'])
    if compile_result.returncode:
        raise SystemExit(f'{mid} does NOT compile (not a kill): {compile_result.stdout}')
    test_result = run(dest, mid + '-test', ['go','test','-count=1','./internal/orchestration','-run','^'+test+'$','-v'])
    killed = test_result.returncode != 0 and assertion in test_result.stdout
    results.append({'id':mid,'test':test,'compiles':True,'killed':killed,'assertion':assertion})
    print(mid, 'KILLED' if killed else 'SURVIVED_OR_WRONG_FAILURE', flush=True)
    if not killed:
        print(test_result.stdout, flush=True)
        break
(OUTPUT / 'result.json').write_text(json.dumps({'baseline':'PASS','sourceDigest':source_digest,'results':results,'allKilled':len(results)==len(MUTANTS) and all(r['killed'] for r in results)},indent=2))
print('MUTATION_EVIDENCE', OUTPUT, flush=True)
raise SystemExit(0 if len(results)==len(MUTANTS) and all(r['killed'] for r in results) else 1)
