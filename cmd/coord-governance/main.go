package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"coord/internal/actioncontext"
)

func main() {
	resolveCmd := flag.NewFlagSet("resolve", flag.ExitOnError)
	projectRoot := resolveCmd.String("project-root", "", "absolute path to project root")
	role := resolveCmd.String("role", "", "agent role (COORDINATOR, EXECUTOR, REVIEWER, AUDITOR)")
	event := resolveCmd.String("event", "", "process event name from ACTION-MAP.yaml")
	assignmentID := resolveCmd.String("assignment-id", "", "assignment identifier")
	baseSHA := resolveCmd.String("base-sha", "", "base git commit SHA")

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: coord-governance <command> [flags]")
		fmt.Fprintln(os.Stderr, "commands: resolve")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "resolve":
		if err := resolveCmd.Parse(os.Args[2:]); err != nil {
			os.Exit(2)
		}
		if *projectRoot == "" || *role == "" || *event == "" || *assignmentID == "" || *baseSHA == "" {
			fmt.Fprintln(os.Stderr, "resolve requires -project-root, -role, -event, -assignment-id, -base-sha")
			os.Exit(2)
		}
		result, err := actioncontext.Resolve(actioncontext.ResolveRequest{
			ProjectRoot:  *projectRoot,
			Role:         actioncontext.Role(*role),
			Event:        actioncontext.Event(*event),
			AssignmentID: *assignmentID,
			BaseSHA:      *baseSHA,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve:", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, "encode:", err)
			os.Exit(1)
		}
		if result.Admission.Status != actioncontext.StatusAdmitted {
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
}
