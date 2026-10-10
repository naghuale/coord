package actioncontext

type registryDocument struct {
	ID                          string         `yaml:"id"`
	Path                        string         `yaml:"path"`
	Class                       string         `yaml:"class"`
	Status                      DocumentStatus `yaml:"status"`
	ReadWhen                    []string       `yaml:"readWhen"`
	CurrentOperationalAuthority *bool          `yaml:"currentOperationalAuthority"`
	CurrentNormativeAuthority   *bool          `yaml:"currentNormativeAuthority"`
}

// authorityReason returns a stable rejection code and explanatory detail.
// The caller decides whether an out-of-event optional document is merely
// ignored or makes admission invalid.
func authorityReason(doc registryDocument, event Event, req ResolveRequest) (string, string) {
	allowed := false
	for _, candidate := range doc.ReadWhen {
		if candidate == string(event) {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrAuthorityViolation, "event_not_allowed"
	}
	if doc.Status == DocStatusHistorical && event != Event("HISTORICAL_INVESTIGATION") {
		return ErrHistoricalNotCurrent, "historical documents are only readable for historical investigation"
	}
	if doc.Status == DocStatusLegacy && !req.LegacyScope {
		return ErrLegacyNotCanonical, "legacy document is outside the requested legacy scope"
	}
	if doc.Status == DocStatusProposed && !req.AllowProposedDirection {
		return ErrProposedNotAccepted, "proposed document is not accepted authority"
	}
	if doc.Class == "active_context" && (doc.CurrentOperationalAuthority == nil || !*doc.CurrentOperationalAuthority) {
		return ErrInactiveContext, "active context document is not current operational authority"
	}
	return "", ""
}
