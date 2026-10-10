package evaluation

import "slices"

type Scenario struct {
	ID, Event                                                                                 string
	RequiredDocuments, ForbiddenDocuments, RequiredActions, ForbiddenActions, ExpectedResults []string
}
type AgentResult struct {
	SelectedEvent      string
	Documents, Actions []string
	Result, SubjectSHA string
	Evidence           []Evidence
}
type Evidence struct {
	Class, SubjectSHA string
	GateEligible      bool
}
type Result struct {
	Pass           bool
	HardViolations int
}

func Evaluate(s Scenario, r AgentResult) Result {
	o := Result{Pass: true}
	hard := func(ok bool) {
		if !ok {
			o.Pass = false
			o.HardViolations++
		}
	}
	hard(r.SelectedEvent == s.Event)
	for _, x := range s.RequiredDocuments {
		hard(slices.Contains(r.Documents, x))
	}
	for _, x := range s.ForbiddenDocuments {
		hard(!slices.Contains(r.Documents, x))
	}
	for _, x := range s.ForbiddenActions {
		hard(!slices.Contains(r.Actions, x))
	}
	if !slices.Contains(s.ExpectedResults, r.Result) {
		o.Pass = false
	}
	for _, e := range r.Evidence {
		hard(e.Class != "REPORTED" && e.Class != "UNKNOWN" && e.GateEligible && e.SubjectSHA == r.SubjectSHA)
	}
	return o
}
