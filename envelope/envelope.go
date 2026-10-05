// Package envelope defines the versioned wire protocol.
package envelope

import (
	"bytes"
	"coord/roles"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type Kind string

const (
	Task       Kind = "TASK"
	Ack        Kind = "ACK"
	Heartbeat  Kind = "HEARTBEAT"
	Result     Kind = "RESULT"
	Ping       Kind = "PING"
	Pong       Kind = "PONG"
	Abort      Kind = "ABORT"
	RoleUpdate Kind = "ROLE_UPDATE"
	RoleAck    Kind = "ROLE_ACK"
)

// Envelope uses typed payloads so decoding never loses payload types.
type Envelope struct {
	SchemaVersion     int               `json:"schema_version"`
	MessageID         string            `json:"message_id"`
	InReplyTo         string            `json:"in_reply_to,omitempty"`
	Timestamp         time.Time         `json:"timestamp"`
	From              string            `json:"from_session"`
	To                string            `json:"to_session"`
	Kind              Kind              `json:"type"`
	Task              string            `json:"task,omitempty"`
	Run               string            `json:"run,omitempty"`
	Attempt           uint64            `json:"attempt,omitempty"`
	TargetSHA         string            `json:"target_sha,omitempty"`
	Fence             uint64            `json:"fence,omitempty"`
	TaskPayload       *TaskPayload      `json:"task_payload,omitempty"`
	AckPayload        *AckPayload       `json:"ack_payload,omitempty"`
	HeartbeatPayload  *HeartbeatPayload `json:"heartbeat_payload,omitempty"`
	ResultPayload     *ResultPayload    `json:"result_payload,omitempty"`
	PingPayload       *PingPayload      `json:"ping_payload,omitempty"`
	PongPayload       *PongPayload      `json:"pong_payload,omitempty"`
	AbortPayload      *AbortPayload     `json:"abort_payload,omitempty"`
	RoleUpdatePayload *roles.Update     `json:"role_update_payload,omitempty"`
	RoleAckPayload    *RoleAckPayload   `json:"role_ack_payload,omitempty"`
}
type RoleAckPayload struct {
	State    string `json:"state"`
	Revision uint64 `json:"revision"`
	Reason   string `json:"reason,omitempty"`
}
type TaskPayload struct {
	AssignedRole *roles.Selection `json:"assigned_role,omitempty"`
	Worktree     string           `json:"worktree"`
	PayloadRef   string           `json:"payload_ref"`
	Deadline     time.Time        `json:"deadline"`
	ProgressPct  int              `json:"progress_pct"`
}
type AckPayload struct {
	State        string    `json:"state"`
	Reason       string    `json:"reason,omitempty"`
	NextUpdateBy time.Time `json:"next_update_by"`
}
type HeartbeatPayload struct {
	State        string    `json:"state"`
	ProgressPct  int       `json:"progress_pct"`
	NextUpdateBy time.Time `json:"next_update_by"`
	Blocker      string    `json:"blocker,omitempty"`
}
type ResultPayload struct {
	Outcome   string   `json:"outcome"`
	Artifacts []string `json:"artifacts,omitempty"`
	Summary   string   `json:"summary"`
}
type PingPayload struct {
	Nonce string `json:"nonce"`
}
type PongPayload struct {
	Nonce  string  `json:"nonce"`
	State  string  `json:"state"`
	Load1m float64 `json:"load_1m"`
}
type AbortPayload struct {
	Reason string `json:"reason"`
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func (e Envelope) Validate() error {
	bad := func(s string) error { return fmt.Errorf("invalid envelope: %s", s) }
	if e.SchemaVersion != 1 || e.MessageID == "" || e.Timestamp.IsZero() || e.From == "" || e.To == "" {
		return bad("missing header or unsupported schema")
	}
	if e.Kind != Ping && e.Kind != Pong && e.Kind != RoleUpdate && e.Kind != RoleAck {
		if e.Task == "" || e.Run == "" || e.Attempt == 0 || e.Fence == 0 {
			return bad("missing run identity")
		}
		if len(e.TargetSHA) != 40 && len(e.TargetSHA) != 64 {
			return bad("full SHA required")
		}
		if _, err := hex.DecodeString(e.TargetSHA); err != nil {
			return bad("invalid SHA")
		}
	}
	n := 0
	for _, present := range []bool{e.TaskPayload != nil, e.AckPayload != nil, e.HeartbeatPayload != nil, e.ResultPayload != nil, e.PingPayload != nil, e.PongPayload != nil, e.AbortPayload != nil, e.RoleUpdatePayload != nil, e.RoleAckPayload != nil} {
		if present {
			n++
		}
	}
	if n != 1 {
		return bad("exactly one payload required")
	}
	if e.Kind == Ack || e.Kind == Heartbeat || e.Kind == Result || e.Kind == Pong || e.Kind == Abort || e.Kind == RoleAck {
		if e.InReplyTo == "" {
			return bad("missing correlation")
		}
	}
	switch e.Kind {
	case RoleAck:
		p := e.RoleAckPayload
		if p == nil || (p.State != "ACCEPTED" && p.State != "REJECTED") || (p.State == "ACCEPTED" && p.Revision == 0) || (p.State == "REJECTED" && p.Reason == "") {
			return bad("ROLE_ACK payload")
		}
	case RoleUpdate:
		if e.RoleUpdatePayload == nil {
			return bad("ROLE_UPDATE payload")
		}
		return e.RoleUpdatePayload.Validate()
	case Task:
		if e.TaskPayload == nil || e.TaskPayload.Worktree == "" || !e.TaskPayload.Deadline.After(e.Timestamp) || e.TaskPayload.ProgressPct < 0 || e.TaskPayload.ProgressPct > 100 {
			return bad("TASK payload")
		}
	case Ack:
		p := e.AckPayload
		if p == nil {
			return bad("ACK payload")
		}
		if p.State != "ACCEPTED" && p.State != "REJECTED" {
			return bad("ACK state")
		}
		if p.State == "ACCEPTED" && !p.NextUpdateBy.After(e.Timestamp) {
			return bad("ACK deadline")
		}
		if p.State == "REJECTED" && p.Reason == "" {
			return bad("rejection reason")
		}
	case Heartbeat:
		p := e.HeartbeatPayload
		if p == nil || (p.State != "STARTED" && p.State != "BLOCKED" && p.State != "RESULT_READY") || p.ProgressPct < 0 || p.ProgressPct > 100 || !p.NextUpdateBy.After(e.Timestamp) {
			return bad("HEARTBEAT payload")
		}
		if p.State == "BLOCKED" && p.Blocker == "" {
			return bad("blocker required")
		}
	case Result:
		p := e.ResultPayload
		if p == nil || (p.Outcome != "SUCCESS" && p.Outcome != "FAILURE" && p.Outcome != "PARTIAL") {
			return bad("RESULT payload")
		}
	case Ping:
		if e.PingPayload == nil || e.PingPayload.Nonce == "" {
			return bad("PING payload")
		}
	case Pong:
		if e.PongPayload == nil || e.PongPayload.Nonce == "" || (e.PongPayload.State != "IDLE" && e.PongPayload.State != "BUSY" && e.PongPayload.State != "DRAINING") || e.PongPayload.Load1m < 0 {
			return bad("PONG payload")
		}
	case Abort:
		if e.AbortPayload == nil || e.AbortPayload.Reason == "" {
			return bad("ABORT payload")
		}
	default:
		return bad("unknown kind")
	}
	return nil
}

func Decode(raw []byte) (Envelope, error) {
	var e Envelope
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return e, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return e, fmt.Errorf("trailing JSON")
	}
	return e, e.Validate()
}
