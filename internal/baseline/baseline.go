package baseline

import (
	"errors"
	"sort"
)

type Identity struct{ Profile, AdapterID, AdapterVersion, AdapterDigest, SuiteDigest, SchemaVersion, MetricVersion string }
type Run struct {
	Identity                     Identity
	Digest                       string
	Metrics                      map[string]float64
	HardViolations, NotEvaluated int
}
type Candidate struct {
	Identity     Identity
	Sources      []string
	Metrics      map[string]float64
	ReviewStatus string
}

func Promote(id Identity, runs []Run, min int) (Candidate, error) {
	v := []Run{}
	for _, r := range runs {
		if r.Identity == id && r.HardViolations == 0 && r.NotEvaluated == 0 {
			v = append(v, r)
		}
	}
	if len(v) < min {
		return Candidate{}, errors.New("BASELINE_NOT_READY")
	}
	m := map[string]float64{}
	for k := range v[0].Metrics {
		a := []float64{}
		for _, r := range v {
			x, ok := r.Metrics[k]
			if !ok {
				a = nil
				break
			}
			a = append(a, x)
		}
		if a == nil {
			continue
		}
		sort.Float64s(a)
		n := len(a)
		if n%2 == 1 {
			m[k] = a[n/2]
		} else {
			m[k] = (a[n/2-1] + a[n/2]) / 2
		}
	}
	s := []string{}
	for _, r := range v {
		s = append(s, r.Digest)
	}
	return Candidate{id, s, m, "REQUIRED"}, nil
}
