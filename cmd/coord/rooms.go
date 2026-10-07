//go:build unix

package main

import (
	"bufio"
	"context"
	"coord/internal/localchat"
	"coord/internal/rooms"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"
)

func roomCommand(command string) bool {
	switch command {
	case "rooms", "room", "invite", "watch", "log", "say", "join", "close", "sessions", "session", "history", "start":
		return true
	}
	return false
}
func roomFlags(name string, args []string, diagnostic io.Writer) (*flag.FlagSet, *string, *string) {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(diagnostic)
	actor := f.String("as", "", "capability участника (агент получает автоматически)")
	role := f.String("role", "", "роль")
	return f, actor, role
}
func runRooms(ctx context.Context, command string, args []string, state, server string, in io.Reader, out, diagnostic io.Writer) error {
	client := localchat.NewClient(state)
	defer client.Close()
	if err := ensure(ctx, state, server, client); err != nil {
		return err
	}
	if command != "session" {
		if err := client.EnableOwner(state); err != nil {
			return err
		}
	}
	if command == "session" || command == "sessions" || command == "history" || command == "start" {
		return runSessionCLI(ctx, client, command, args, state, in, out, diagnostic)
	}
	switch command {
	case "rooms":
		if len(args) != 0 {
			return fmt.Errorf("coord rooms")
		}
		response, err := client.Call(ctx, "rooms", localchat.Request{})
		if err != nil {
			return err
		}
		if len(response.Rooms) == 0 {
			_, err = fmt.Fprintln(out, "Комнат пока нет. coord room create <задача>")
			return err
		}
		for _, r := range response.Rooms {
			status := "active"
			if r.Closed {
				status = "closed"
			}
			active := 0
			for _, p := range r.Members {
				if p.Active {
					active++
				}
			}
			if _, err = fmt.Fprintf(out, "%s · %s · %s · agents=%d · pending=%d · errors=%d\n", r.ID, r.Task, status, active, r.Pending, r.DeliveryErrors); err != nil {
				return err
			}
		}
	case "room":
		if len(args) < 2 || args[0] != "create" {
			return fmt.Errorf("coord room create <задача> [--title название]")
		}
		flags, actor, _ := roomFlags("room create", args[2:], diagnostic)
		title := flags.String("title", args[1], "название совместной задачи")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		response, err := client.Call(ctx, "room-create", localchat.Request{Task: args[1], Title: *title, Actor: *actor})
		if err != nil {
			return err
		}
		if response.Room == nil {
			return fmt.Errorf("комната не создана")
		}
		_, err = fmt.Fprintln(out, response.Room.ID)
		return err
	case "invite":
		if len(args) < 2 {
			return fmt.Errorf("coord invite <комната> <агент> --role <роль> [--agent-id stable-id]")
		}
		flags, actor, role := roomFlags("invite", args[2:], diagnostic)
		agentID := flags.String("agent-id", "", "стабильная identity для независимости auditor")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *role == "" {
			return fmt.Errorf("укажите --role coordinator|executor|reviewer|auditor")
		}
		_, err := client.Call(ctx, "invite", localchat.Request{Room: args[0], Value: args[1], Role: *role, AgentID: *agentID, Actor: *actor})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Приглашение сохранено и поставлено в очередь. Участник станет active после подтверждения.")
		return err
	case "say":
		if len(args) < 2 {
			return fmt.Errorf("coord say <комната> [--as token] <реплика> или --stdin")
		}
		flags, actor, _ := roomFlags("say", args[1:], diagnostic)
		stdin := flags.Bool("stdin", false, "читать реплику из stdin")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		text := strings.Join(flags.Args(), " ")
		if *stdin {
			if flags.NArg() != 0 {
				return fmt.Errorf("текст и --stdin нельзя использовать вместе")
			}
			raw, err := io.ReadAll(io.LimitReader(in, 8193))
			if err != nil {
				return err
			}
			text = string(raw)
		}
		response, err := client.Call(ctx, "say", localchat.Request{Room: args[0], Text: text, Actor: *actor})
		if err != nil {
			return err
		}
		if response.Event == nil {
			return fmt.Errorf("реплика не записана")
		}
		_, err = fmt.Fprintf(out, "Записано: #%d (%s).\n", response.Event.Seq, response.Event.Role)
		return err
	case "close":
		if len(args) < 1 {
			return fmt.Errorf("coord close <комната>")
		}
		flags, actor, _ := roomFlags("close", args[1:], diagnostic)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		if _, err := client.Call(ctx, "room-close", localchat.Request{Room: args[0], Actor: *actor}); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "Комната закрыта. Это не означает approval или merge результата задачи.")
		return err
	case "log", "watch":
		if len(args) != 1 {
			return fmt.Errorf("coord %s <комната>", command)
		}
		if command == "log" {
			response, err := client.Call(ctx, "events", localchat.Request{Room: args[0]})
			if err != nil {
				return err
			}
			for _, event := range response.Events {
				if err = printEvent(out, event); err != nil {
					return err
				}
			}
			return nil
		}
		return client.Watch(ctx, args[0], 0, func(e rooms.Event) error { return printEvent(out, e) })
	case "join":
		if len(args) < 1 {
			return fmt.Errorf("coord join <комната> [--role owner]")
		}
		flags, _, role := roomFlags("join", args[1:], diagnostic)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || (*role != "" && *role != "owner") {
			return fmt.Errorf("owner входит через join; AI участники входят по приглашению")
		}
		return roomChat(ctx, client, args[0], in, out)
	case "sessions":
		response, err := client.Call(ctx, "peers", localchat.Request{})
		if err != nil {
			return err
		}
		lists, err := client.Call(ctx, "rooms", localchat.Request{})
		if err != nil {
			return err
		}
		for _, p := range response.Peers {
			var memberships []string
			for _, r := range lists.Rooms {
				if !r.Closed {
					for _, member := range r.Members {
						if member.Ref == p.Ref {
							status := "invited"
							if member.Active {
								status = "active"
							}
							memberships = append(memberships, r.Task+":"+member.Role+":"+status)
						}
					}
				}
			}
			if _, err = fmt.Fprintf(out, "%s [%s] · %s · %s\n", p.Address, p.Ref, p.Activity, strings.Join(memberships, ", ")); err != nil {
				return err
			}
		}
	}
	return nil
}
func printEvent(out io.Writer, event rooms.Event) error {
	_, err := fmt.Fprintf(out, "#%d %s [%s / %s] %s\n", event.Seq, event.At.Local().Format("15:04:05"), event.Role, event.Agent, event.Text)
	return err
}
func roomChat(ctx context.Context, client *localchat.Client, id string, in io.Reader, out io.Writer) error {
	// Validate before starting an input goroutine.
	if _, err := client.Call(ctx, "events", localchat.Request{Room: id}); err != nil {
		return err
	}
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	var output sync.Mutex
	if _, err := fmt.Fprintln(out, "Вы в комнате как owner. Пишите реплики; /quit — выход."); err != nil {
		return err
	}
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- client.Watch(life, id, 0, func(e rooms.Event) error { output.Lock(); defer output.Unlock(); return printEvent(out, e) })
	}()
	input := make(chan string)
	readDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 1024), 8192)
		for scanner.Scan() {
			select {
			case input <- scanner.Text():
			case <-life.Done():
				return
			}
		}
		readDone <- scanner.Err()
	}()
	for {
		select {
		case err := <-watchDone:
			return err
		case err := <-readDone:
			cancel()
			<-watchDone
			return err
		case <-ctx.Done():
			cancel()
			<-watchDone
			return ctx.Err()
		case text := <-input:
			text = strings.TrimSpace(text)
			if text == "/quit" {
				cancel()
				<-watchDone
				return nil
			}
			if text == "" {
				continue
			}
			_, err := client.Call(life, "say", localchat.Request{Room: id, Text: text})
			if err != nil {
				cancel()
				watchErr := <-watchDone
				return errors.Join(err, watchErr)
			}
		}
	}
}
