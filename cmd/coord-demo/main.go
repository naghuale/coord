package main

import (
	"context"
	"coord"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/store"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type local struct {
	in  chan envelope.Envelope
	out chan envelope.Envelope
}

func (l *local) WhoAmI() string                                   { return "session://coordinator" }
func (l *local) Receive(context.Context) <-chan envelope.Envelope { return l.in }
func (l *local) Send(ctx context.Context, _ string, e envelope.Envelope) error {
	select {
	case l.out <- e:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func main() {
	dir, err := os.MkdirTemp("", "coord-demo-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	s, err := store.New(checkpoint.File{Path: filepath.Join(dir, "runs.json")})
	if err != nil {
		log.Fatal(err)
	}
	tp := &local{make(chan envelope.Envelope, 8), make(chan envelope.Envelope, 8)}
	c, err := coord.New(tp, s, coord.DefaultConfig())
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, 1)
	go func() { errs <- c.Run(ctx) }()
	r := fsm.Run{Task: "CF-009", RunID: "CF-009-run-1", Attempt: 3, Fence: 1, TargetSHA: strings.Repeat("a", 40), OwnerSession: "session://executor", Worktree: dir, Deadline: time.Now().Add(time.Hour)}
	if err = c.Assign(ctx, r); err != nil {
		log.Fatal(err)
	}
	task := <-tp.out
	reply := func(k envelope.Kind) envelope.Envelope {
		e := task
		e.Kind = k
		e.MessageID = envelope.NewID()
		e.InReplyTo = task.MessageID
		e.Timestamp = time.Now().UTC()
		e.From, e.To = task.To, task.From
		e.TaskPayload = nil
		return e
	}
	ack := reply(envelope.Ack)
	ack.AckPayload = &envelope.AckPayload{State: "ACCEPTED", NextUpdateBy: time.Now().Add(time.Minute)}
	tp.in <- ack
	for _, state := range []string{"STARTED", "RESULT_READY"} {
		e := reply(envelope.Heartbeat)
		e.HeartbeatPayload = &envelope.HeartbeatPayload{State: state, ProgressPct: 100, NextUpdateBy: time.Now().Add(time.Minute)}
		tp.in <- e
	}
	result := reply(envelope.Result)
	result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "demo complete"}
	tp.in <- result
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			run, _ := s.Get(r.RunID)
			if run.Status == fsm.Done {
				fmt.Printf("%s: %s (snapshot persisted)\n", run.RunID, run.Status)
				cancel()
				<-errs
				return
			}
		case err := <-errs:
			log.Fatal(err)
		case <-ctx.Done():
			log.Fatal("demo timed out")
		}
	}
}
