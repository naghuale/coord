//go:build unix

package main

import (
	"coord/checkpoint"
	"coord/store"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type assignmentStatusError struct {
	reason string
	code   int
}

func (e *assignmentStatusError) Error() string { return e.reason }
func (e *assignmentStatusError) ExitCode() int { return e.code }

// Read the same atomic canonical.json used by coordd, including while its
// writer owns the namespace. No ownership lock or migration is requested.
func runAssignmentStatus(args []string, state string, out io.Writer, now time.Time) error {
	if len(args) < 2 || args[0] != "status" || args[1] == "" {
		return fmt.Errorf("coord [--state <coordd-state-dir>] assignment status <id> [--format json|yaml]")
	}
	id, format := args[1], "json"
	if len(args) == 4 && args[2] == "--format" {
		format = args[3]
	} else if len(args) != 2 {
		return fmt.Errorf("assignment status: invalid arguments")
	}
	if format != "json" && format != "yaml" {
		return fmt.Errorf("assignment status: --format json|yaml required")
	}
	status := store.AssignmentStatus{AssignmentID: id}
	var failure error
	raw, err := os.ReadFile(filepath.Join(state, "canonical.json"))
	if err != nil {
		status.Reason = "STORE_UNAVAILABLE"
		failure = &assignmentStatusError{status.Reason, 4}
	} else {
		snapshot, err := checkpoint.DecodeCanonicalSnapshot(raw)
		if err != nil || snapshot.SchemaVersion != store.CanonicalSchemaVersion {
			status.Reason = "STORE_CORRUPT"
			failure = &assignmentStatusError{status.Reason, 5}
		} else {
			status, err = store.InspectAssignment(snapshot, id, now)
			if err != nil {
				failure = &assignmentStatusError{status.Reason, 3}
			}
		}
	}
	// JSON field names are the contract in either representation.
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	if format == "yaml" {
		var object yaml.Node
		if err := yaml.Unmarshal(data, &object); err != nil {
			return err
		}
		data, err = yaml.Marshal(&object)
		if err != nil {
			return err
		}
	} else {
		data = append(data, '\n')
	}
	if _, err := out.Write(data); err != nil {
		return err
	}
	return failure
}
