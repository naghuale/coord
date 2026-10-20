package main

import (
	"context"
	"coord/internal/shadowpilot"
	"coord/telepathy"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

const shadowDomain = "shadow_validation"

// ShadowRefusal is a fail-closed rejection before completion or evidence sealing.
type ShadowRefusal struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (e *ShadowRefusal) Error() string { return "shadow pilot refused: " + e.Field + ": " + e.Reason }

func refuse(field, reason string) error { return &ShadowRefusal{Field: field, Reason: reason} }

type shadowDispatch struct {
	CommandDomain string                       `json:"commandDomain"`
	DispatchID    string                       `json:"dispatchID"`
	ShadowRunID   shadowpilot.ShadowRunID      `json:"shadowRunID"`
	AssignmentID  string                       `json:"assignmentID"`
	SubjectSHA    string                       `json:"subjectSHA"`
	BaselineSHA   string                       `json:"baselineSHA"`
	Generation    uint64                       `json:"generation"`
	InputDigest   string                       `json:"inputDigest"`
	ReadOnly      bool                         `json:"readOnly"`
	Assignment    shadowpilot.ShadowAssignment `json:"assignment"`
	Invariants    []shadowInvariant            `json:"invariants"`
	ReplyTo       string                       `json:"replyTo"`
	InputFixture  string                       `json:"inputFixture"`
}

type shadowResult struct {
	CommandDomain string                  `json:"commandDomain"`
	DispatchID    string                  `json:"dispatchID"`
	ShadowRunID   shadowpilot.ShadowRunID `json:"shadowRunID"`
	BaselineSHA   string                  `json:"baselineSHA"`
	ResultDigest  string                  `json:"resultDigest"`
	Result        shadowpilot.AgentResult `json:"result"`
}

type shadowAgentAdapter interface {
	Dispatch(context.Context, shadowDispatch) error
	Receive(context.Context, string) (shadowResult, error)
}

// Telepathy details remain entirely in the CLI, outside the model package.
// The production constructor owns a verified MCP Process, not a prompt callback.
type telepathyShadowAgentAdapter struct {
	tools   telepathy.Tools
	process *telepathy.Process
	to      string
	replyTo string
	pending shadowDispatch
}

func shadowReplyAddress(text string) (string, error) {
	first, _, _ := strings.Cut(text, "\n")
	tail, ok := strings.CutPrefix(first, "This session is ")
	address, ref, found := strings.Cut(tail, " [")
	if !ok || !found || !strings.Contains(address, ":") || !strings.HasSuffix(ref, "].") || strings.ContainsAny(address, " \t\r\n") {
		return "", refuse("adapter", "Telepathy did not identify the sender session")
	}
	return address, nil
}

func (a *telepathyShadowAgentAdapter) Dispatch(ctx context.Context, dispatch shadowDispatch) error {
	if a == nil || nilMCPClient(a.tools) {
		return refuse("adapter", "non-nil MCP client required")
	}
	raw, err := json.Marshal(dispatch)
	if err != nil {
		return err
	}
	message := "READ-ONLY SHADOW VALIDATION. No production, canonical store, GitHub, repository or credential writes. Verify the supplied immutable fixture invariants; do not claim independent review/audit. Return only a typed JSON shadow result to replyTo via send_message, between BEGIN_COORD_SHADOW_RESULT and END_COORD_SHADOW_RESULT. Echo commandDomain, dispatchID, shadowRunID, baselineSHA; include resultDigest = SHA256 of compact Go AgentResult JSON (snake_case fields, findings sorted by ID). Result fields must match assignment exactly; kind=AgentResult. Do not use free-text success.\nBEGIN_COORD_SHADOW_ASSIGNMENT\n" + string(raw) + "\nEND_COORD_SHADOW_ASSIGNMENT"
	_, err = a.tools.Call(ctx, "send_message", map[string]any{"to": a.to, "message": message})
	if err == nil {
		a.pending = dispatch
	}
	return err
}

func decodeShadowResult(raw string) (shadowResult, error) {
	var result shadowResult
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return result, refuse("result_schema", err.Error())
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return result, refuse("result_schema", "exactly one JSON object required")
	}
	return result, nil
}

func (a *telepathyShadowAgentAdapter) Receive(ctx context.Context, dispatchID string) (shadowResult, error) {
	if a == nil || nilMCPClient(a.tools) {
		return shadowResult{}, refuse("adapter", "non-nil MCP client required")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		text, err := a.tools.Call(ctx, "read_messages", map[string]any{"limit": 50})
		if err != nil {
			return shadowResult{}, err
		}
		for {
			_, tail, found := strings.Cut(text, "BEGIN_COORD_SHADOW_RESULT\n")
			if !found {
				break
			}
			raw, rest, closed := strings.Cut(tail, "\nEND_COORD_SHADOW_RESULT")
			if !closed {
				return shadowResult{}, refuse("result_schema", "unterminated result envelope")
			}
			text = rest
			result, err := decodeShadowResult(raw)
			if err != nil {
				return result, err
			}
			if result.DispatchID == dispatchID || (a.pending.AssignmentID != "" && result.Result.AssignmentID == a.pending.AssignmentID) || (a.pending.ShadowRunID != "" && result.ShadowRunID == a.pending.ShadowRunID) {
				return result, nil
			}
		}
		select {
		case <-ctx.Done():
			return shadowResult{}, fmt.Errorf("correlated AgentResult not received: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// validateShadowResult precedes Complete and sealing. Equality is exact, including
// immutable SHAs and canonical AgentID, rather than case-folded or trimmed.
func validateShadowResult(d shadowDispatch, r shadowResult) error {
	a, result := d.Assignment, r.Result
	for _, field := range []struct{ name, actual, expected string }{
		{"commandDomain", r.CommandDomain, shadowDomain},
		{"dispatchID", r.DispatchID, d.DispatchID},
		{"shadowRunID", string(r.ShadowRunID), string(a.ShadowRunID)},
		{"baselineSHA", r.BaselineSHA, d.BaselineSHA},
		{"assignmentID", result.AssignmentID, a.AssignmentID},
		{"agentID", result.AgentID, a.AgentID},
		{"sessionID", result.SessionID, a.SessionID},
		{"subjectSHA", result.SubjectSHA, a.SubjectSHA},
		{"inputDigest", result.InputDigest, a.InputDigest},
	} {
		if field.actual != field.expected {
			return refuse(field.name, "does not match exact assignment")
		}
	}
	if result.Generation != a.Generation {
		return refuse("generation", "does not match exact assignment")
	}
	if err := result.Validate(); err != nil {
		return refuse("result_schema", err.Error())
	}
	digest, err := shadowpilot.AgentResultDigest(result)
	if err != nil {
		return err
	}
	if digest != r.ResultDigest {
		return refuse("resultDigest", "digest mismatch")
	}
	return nil
}

func closeShadowAdapter(a shadowAgentAdapter) error {
	if live, ok := a.(*telepathyShadowAgentAdapter); ok && live != nil && live.process != nil {
		return live.process.Close()
	}
	return nil
}

// Only a concrete Telepathy adapter can enter the live runner. A test adapter
// cannot claim to be live by returning a string such as "telepathy".
func nilMCPClient(tools telepathy.Tools) bool {
	if tools == nil {
		return true
	}
	v := reflect.ValueOf(tools)
	return (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil()
}

func requireLiveShadowAdapter(a shadowAgentAdapter) error {
	live, ok := a.(*telepathyShadowAgentAdapter)
	if !ok || live == nil || nilMCPClient(live.tools) {
		return refuse("adapter", "live pilot requires concrete Telepathy adapter and non-nil MCP client")
	}
	return nil
}
