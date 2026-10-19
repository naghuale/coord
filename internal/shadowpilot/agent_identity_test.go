package shadowpilot

import "testing"

func TestNormalizeAgentID(t *testing.T) {
	for _, id := range []string{"agent-executor", "opencode:workspace-ob5"} {
		got, err := normalizeAgentID(id)
		if err != nil || got != id {
			t.Fatalf("normalizeAgentID(%q) = %q, %v", id, got, err)
		}
	}
	for _, id := range []string{"", " \t\n\u00a0", " agent-executor", "agent-executor\u00a0", "\tagent-executor\n", "AGENT-EXECUTOR"} {
		if got, err := normalizeAgentID(id); err == nil || got != "" {
			t.Errorf("normalizeAgentID(%q) = %q, %v; want empty ID and error", id, got, err)
		}
	}
}
