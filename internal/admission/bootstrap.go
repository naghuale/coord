package admission

import "encoding/json"

type AgentBootstrap struct {
	APIVersion          string           `json:"apiVersion"`
	Kind                string           `json:"kind"`
	BootstrapValidation BootstrapResult  `json:"bootstrapValidation"`
	ActionContext       json.RawMessage  `json:"actionContext"`
	WorkspaceBinding    WorkspaceBinding `json:"workspaceBinding"`
	Session             SessionBinding   `json:"session"`
	Assignment          Assignment       `json:"assignment"`
}

type BootstrapResult struct {
	Result string `json:"result"`
}

type WorkspaceBinding struct {
	ProjectRoot      string `json:"projectRoot"`
	RepositoryRoot   string `json:"repositoryRoot"`
	AssignedWorktree string `json:"assignedWorktree"`
}

type SessionBinding struct {
	Role    string `json:"role"`
	AgentID string `json:"agentId"`
}

type Assignment struct {
	AssignmentID string `json:"assignmentId"`
	BaseSHA      string `json:"baseSha"`
}
