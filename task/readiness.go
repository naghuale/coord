package task

import "coord/taskid"

// ReadinessStatus is the gate-readiness of a Task computed from its event log.
type ReadinessStatus string

const (
	ReadinessReady           ReadinessStatus = "READY"
	ReadinessChangesRequired ReadinessStatus = "CHANGES_REQUIRED"
	ReadinessAuditRequired   ReadinessStatus = "AUDIT_REQUIRED"
	ReadinessAuditBlocked    ReadinessStatus = "AUDIT_BLOCKED"
	ReadinessNotEvaluated    ReadinessStatus = "NOT_EVALUATED"
)

// ReadinessEvaluation is the result of EvaluateReadiness.
type ReadinessEvaluation struct {
	TaskID         taskid.ID
	Status         ReadinessStatus
	ReviewResultID string   // ResultID of the latest non-superseded ReviewResult; empty when NOT_EVALUATED
	AuditResultID  string   // ResultID of the latest non-superseded AuditResult; empty when AUDIT_REQUIRED or NOT_EVALUATED
	SupersededIDs  []string // all ResultIDs that were superseded at evaluation time
}

// EvaluateReadiness computes the gate-readiness of a Task from its ordered
// event log. It implements the notEvaluatedIsNotPass invariant: absence of a
// non-superseded ReviewResult always produces NOT_EVALUATED, never READY.
//
// The caller must pass only events for the given taskID; EvaluateReadiness
// does not filter by TaskID and will silently include foreign events.
func EvaluateReadiness(taskID taskid.ID, events []DomainEvent) ReadinessEvaluation {
	eval := ReadinessEvaluation{
		TaskID:        taskID,
		SupersededIDs: []string{},
	}

	// Collect superseded ResultIDs.
	superseded := make(map[string]bool)
	for _, e := range events {
		if e.Type == ReviewResultSuperseded || e.Type == AuditResultSuperseded {
			if e.SupersededResultID != "" {
				superseded[e.SupersededResultID] = true
				eval.SupersededIDs = append(eval.SupersededIDs, e.SupersededResultID)
			}
		}
	}

	// Find latest non-superseded ReviewResult (highest Round wins).
	var latestReview *ReviewResult
	var latestReviewRound uint32
	for i := range events {
		e := &events[i]
		if e.Type != ReviewResultRecorded || e.ReviewResult == nil {
			continue
		}
		rr := e.ReviewResult
		if superseded[rr.ResultID] {
			continue
		}
		if latestReview == nil || rr.Round > latestReviewRound {
			latestReview = rr
			latestReviewRound = rr.Round
		}
	}

	// notEvaluatedIsNotPass invariant.
	if latestReview == nil {
		eval.Status = ReadinessNotEvaluated
		return eval
	}
	eval.ReviewResultID = latestReview.ResultID

	if latestReview.Verdict == ReviewChangesRequired {
		eval.Status = ReadinessChangesRequired
		return eval
	}

	// Review is APPROVED — check audit.
	var latestAudit *AuditResult
	var latestAuditRound uint32
	for i := range events {
		e := &events[i]
		if e.Type != AuditResultRecorded || e.AuditResult == nil {
			continue
		}
		ar := e.AuditResult
		if superseded[ar.ResultID] {
			continue
		}
		if latestAudit == nil || ar.Round > latestAuditRound {
			latestAudit = ar
			latestAuditRound = ar.Round
		}
	}

	if latestAudit == nil {
		eval.Status = ReadinessAuditRequired
		return eval
	}
	eval.AuditResultID = latestAudit.ResultID

	switch latestAudit.Verdict {
	case AuditPass:
		eval.Status = ReadinessReady
	case AuditChangesRequired:
		eval.Status = ReadinessChangesRequired
	case AuditBlocked:
		eval.Status = ReadinessAuditBlocked
	default:
		eval.Status = ReadinessNotEvaluated
	}
	return eval
}
