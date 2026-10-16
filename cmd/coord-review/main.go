//go:build unix

// coord-review prints read-only projections of canonical Review, Audit,
// Discussion and Gate state from a local canonical store file.
package main

import (
	"coord/checkpoint"
	"coord/task"
	"coord/taskid"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

const usage = `Read-only projections of Review, Audit, Discussion and Gate state.

  coord-review review show --store PATH --task-id ID
  coord-review audit show  --store PATH --task-id ID
  coord-review discussion show --store PATH --task-id ID
  coord-review gate evaluate --store PATH --task-id ID

All commands print JSON to stdout and exit 0 on success.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	// Parse top-level command (e.g. "review show", "gate evaluate").
	// args[0] = noun, args[1] = verb.
	noun, verb := args[0], args[1]
	rest := args[2:]

	fs := flag.NewFlagSet(noun+" "+verb, flag.ContinueOnError)
	storePath := fs.String("store", "", "path to canonical.json (required)")
	taskIDStr := fs.String("task-id", "", "task ID (required)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *storePath == "" || *taskIDStr == "" {
		fs.Usage()
		return fmt.Errorf("--store and --task-id are required")
	}

	id, err := taskid.Parse(*taskIDStr)
	if err != nil {
		return fmt.Errorf("invalid task-id: %w", err)
	}

	cf := checkpoint.CanonicalFile{Path: *storePath}
	// Load performs in-memory normalization (NormalizeCanonicalState) but no
	// durable write or schema migration. Output is a projection of the
	// normalized snapshot; schema_version reflects the in-memory value only.
	state, err := cf.Load()
	if err != nil {
		return fmt.Errorf("loading store: %w", err)
	}

	// Write snapshot provenance to stderr so callers can distinguish
	// in-memory normalization from a persisted migration.
	fmt.Fprintf(os.Stderr, "snapshot: schema_version=%d path=%s\n", state.SchemaVersion, *storePath)

	// Collect events for this task.
	var events []task.DomainEvent
	for _, e := range state.Events {
		if e.TaskID == id {
			events = append(events, e)
		}
	}

	switch noun {
	case "review":
		if verb != "show" {
			return fmt.Errorf("unknown review verb %q; want show", verb)
		}
		return runReviewShow(id, events)
	case "audit":
		if verb != "show" {
			return fmt.Errorf("unknown audit verb %q; want show", verb)
		}
		return runAuditShow(id, events)
	case "discussion":
		if verb != "show" {
			return fmt.Errorf("unknown discussion verb %q; want show", verb)
		}
		return runDiscussionShow(id, events)
	case "gate":
		if verb != "evaluate" {
			return fmt.Errorf("unknown gate verb %q; want evaluate", verb)
		}
		return runGateEvaluate(id, events)
	default:
		return fmt.Errorf("unknown command %q %q; see usage", noun, verb)
	}
}

type reviewProjection struct {
	TaskID        string              `json:"task_id"`
	Results       []task.ReviewResult `json:"results"`
	SupersededIDs []string            `json:"superseded_ids"`
}

func runReviewShow(id taskid.ID, events []task.DomainEvent) error {
	superseded := supersededSet(events, task.ReviewResultSuperseded)
	var results []task.ReviewResult
	for _, e := range events {
		if e.Type == task.ReviewResultRecorded && e.ReviewResult != nil {
			rr := *e.ReviewResult
			rr.ResultID = e.ReviewResult.ResultID
			results = append(results, rr)
		}
	}
	if results == nil {
		results = []task.ReviewResult{}
	}
	supersededList := supersededSlice(superseded)
	return printJSON(reviewProjection{
		TaskID:        id.String(),
		Results:       results,
		SupersededIDs: supersededList,
	})
}

type auditProjection struct {
	TaskID        string             `json:"task_id"`
	Results       []task.AuditResult `json:"results"`
	SupersededIDs []string           `json:"superseded_ids"`
}

func runAuditShow(id taskid.ID, events []task.DomainEvent) error {
	superseded := supersededSet(events, task.AuditResultSuperseded)
	var results []task.AuditResult
	for _, e := range events {
		if e.Type == task.AuditResultRecorded && e.AuditResult != nil {
			results = append(results, *e.AuditResult)
		}
	}
	if results == nil {
		results = []task.AuditResult{}
	}
	return printJSON(auditProjection{
		TaskID:        id.String(),
		Results:       results,
		SupersededIDs: supersededSlice(superseded),
	})
}

type discussionProjection struct {
	TaskID string                `json:"task_id"`
	Nodes  []task.DiscussionNode `json:"nodes"`
	Edges  []task.DiscussionEdge `json:"edges"`
}

func runDiscussionShow(id taskid.ID, events []task.DomainEvent) error {
	var nodes []task.DiscussionNode
	var edges []task.DiscussionEdge
	for _, e := range events {
		if e.Type == task.DiscussionNodeRecorded && e.DiscussionNode != nil {
			nodes = append(nodes, *e.DiscussionNode)
		}
		if e.Type == task.DiscussionEdgeRecorded && e.DiscussionEdge != nil {
			edges = append(edges, *e.DiscussionEdge)
		}
	}
	if nodes == nil {
		nodes = []task.DiscussionNode{}
	}
	if edges == nil {
		edges = []task.DiscussionEdge{}
	}
	return printJSON(discussionProjection{TaskID: id.String(), Nodes: nodes, Edges: edges})
}

func runGateEvaluate(id taskid.ID, events []task.DomainEvent) error {
	eval := task.EvaluateReadiness(id, events)
	return printJSON(eval)
}

func supersededSet(events []task.DomainEvent, et task.EventType) map[string]bool {
	m := make(map[string]bool)
	for _, e := range events {
		if e.Type == et && e.SupersededResultID != "" {
			m[e.SupersededResultID] = true
		}
	}
	return m
}

func supersededSlice(m map[string]bool) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	return s
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
