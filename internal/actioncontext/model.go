package actioncontext

// Role identifies the agent role for which context is resolved.
type Role string

const (
	RoleCoordinator Role = "COORDINATOR"
	RoleExecutor    Role = "EXECUTOR"
	RoleReviewer    Role = "REVIEWER"
	RoleAuditor     Role = "AUDITOR"
)

// Event is a process event named in the Action Map.
type Event string

type AdmissionStatus string

const (
	StatusAdmitted         AdmissionStatus = "ADMITTED"
	StatusInvalidAdmission AdmissionStatus = "INVALID_ADMISSION"
)

type DocumentStatus string

const (
	DocStatusActive     DocumentStatus = "active"
	DocStatusProposed   DocumentStatus = "proposed"
	DocStatusLegacy     DocumentStatus = "legacy"
	DocStatusHistorical DocumentStatus = "historical"
)

const (
	ErrUnknownEvent             = "UNKNOWN_EVENT"
	ErrDocumentUnregistered     = "DOCUMENT_UNREGISTERED"
	ErrRequiredDocumentMissing  = "REQUIRED_DOCUMENT_MISSING"
	ErrAuthorityViolation       = "AUTHORITY_VIOLATION"
	ErrContextBudgetExceeded    = "CONTEXT_BUDGET_EXCEEDED"
	ErrDigestDrift              = "DIGEST_DRIFT"
	ErrPathEscape               = "PATH_ESCAPE"
	ErrHistoricalNotCurrent     = "HISTORICAL_NOT_CURRENT"
	ErrLegacyNotCanonical       = "LEGACY_NOT_CANONICAL"
	ErrProposedNotAccepted      = "PROPOSED_NOT_ACCEPTED"
	ErrInactiveContext          = "INACTIVE_CONTEXT"
	ErrHistoricalBudgetExceeded = "HISTORICAL_BUDGET_EXCEEDED"
	ErrSubjectBindingMissing    = "SUBJECT_BINDING_MISSING"
)

type AuthorityRejection struct {
	DocumentID string `json:"documentId"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
}

type DocumentBinding struct {
	ID     string         `json:"id"`
	Path   string         `json:"path"`
	Digest string         `json:"digest"`
	Status DocumentStatus `json:"status"`
}

type ContextBudget struct {
	MaxDocuments           int `json:"maxDocuments" yaml:"maxDocuments"`
	MaxHistoricalDocuments int `json:"maxHistoricalDocuments" yaml:"maxHistoricalDocuments"`
	MaxInputTokens         int `json:"maxInputTokens" yaml:"maxInputTokens"`
}

type Admission struct {
	Status     AdmissionStatus      `json:"status"`
	Rejections []AuthorityRejection `json:"rejections,omitempty"`
	Documents  []DocumentBinding    `json:"documents,omitempty"`
	Missing    []string             `json:"missing,omitempty"`
}

type ResolveRequest struct {
	ProjectRoot            string
	Role                   Role
	Event                  Event
	AssignmentID           string
	BaseSHA                string
	OwningDocumentIDs      []string
	AdditionalDocumentIDs  []string
	RegistryPath           string
	ActionMapPath          string
	BudgetsPath            string
	AllowProposedDirection bool
	LegacyScope            bool
}

type ResolveResult struct {
	Admission Admission `json:"admission"`
}
