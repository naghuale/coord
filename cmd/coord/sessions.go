//go:build unix

package main

import (
	"context"
	"coord/internal/atomicfile"
	"coord/internal/localchat"
	"coord/internal/rooms"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func credentialPath(state string, a rooms.SessionAuth) string {
	// Session IDs originate in Coord; basename also protects CLI file construction.
	return filepath.Join(state, "clients", fmt.Sprintf("%s-%d.json", filepath.Base(a.Session), a.Generation))
}
func saveAttachment(state string, a *rooms.Attachment, out io.Writer) error {
	path := credentialPath(state, a.Auth)
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicfile.Write(path, raw); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "session=%s generation=%d role=%s\ncredential-file=%s\n", a.Auth.Session, a.Auth.Generation, a.Context.Member.Role, path)
	return err
}
func loadAuth(path, session string) (rooms.SessionAuth, error) {
	if path == "" {
		return rooms.SessionAuth{}, fmt.Errorf("укажите --credential-file из ответа подключения")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return rooms.SessionAuth{}, err
	}
	var a rooms.Attachment
	if err = json.Unmarshal(raw, &a); err != nil {
		return rooms.SessionAuth{}, err
	}
	if (session != "" && a.Auth.Session != session) || a.Auth.Token == "" {
		return rooms.SessionAuth{}, fmt.Errorf("credential belongs to another session")
	}
	return a.Auth, nil
}
func runSessionCLI(ctx context.Context, client *localchat.Client, command string, args []string, state string, in io.Reader, out, diagnostic io.Writer) error {
	encode := func(v any) error { return json.NewEncoder(out).Encode(v) }
	if command == "start" {
		if len(args) < 1 {
			return fmt.Errorf("coord start PROJECT --coordinator PEER --agent-id ID --operation ID")
		}
		f := flag.NewFlagSet("start", flag.ContinueOnError)
		f.SetOutput(diagnostic)
		peer := f.String("coordinator", "", "существующая сессия координатора")
		agent := f.String("agent-id", "", "стабильная identity координатора")
		operation := f.String("operation", "", "ID owner-приглашения")
		instructions := f.String("instructions", "Coordinate only this project; invite agents, assign roles, preserve checkpoints. Do not impersonate owner or execute code without a separate role session.", "регламент координатора")
		title := f.String("title", args[0], "название проекта")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *peer == "" || *agent == "" || *operation == "" {
			return fmt.Errorf("укажите coordinator, agent-id и operation")
		}
		r, err := client.Call(ctx, "owner-start", localchat.Request{Task: args[0], Title: *title, AgentID: *agent, Value: *peer, Instructions: *instructions, Operation: *operation})
		if err != nil {
			return err
		}
		return encode(r.Start)
	}
	if command == "sessions" {
		if len(args) != 0 {
			return fmt.Errorf("coord sessions")
		}
		r, err := client.Call(ctx, "sessions", localchat.Request{})
		if err != nil {
			return err
		}
		return encode(r.Sessions)
	}
	if command == "history" {
		if len(args) < 1 {
			return fmt.Errorf("coord history status|export [--content]")
		}
		if args[0] == "status" && len(args) == 1 {
			r, err := client.Call(ctx, "history-status", localchat.Request{})
			if err != nil {
				return err
			}
			return encode(r.History)
		}
		if args[0] != "export" {
			return fmt.Errorf("coord history status|export")
		}
		f := flag.NewFlagSet("history export", flag.ContinueOnError)
		f.SetOutput(diagnostic)
		content := f.Bool("content", false, "явно включить текст и checkpoints в локальную Git-историю")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		r, err := client.Call(ctx, "history-export", localchat.Request{IncludeContent: *content})
		if err != nil {
			return err
		}
		return encode(r.History)
	}
	if len(args) < 1 {
		return fmt.Errorf("coord session assign|invite|register|registration|resume|context|ready|say|ack|checkpoint")
	}
	action, id := args[0], ""
	remaining := args[1:]
	if action != "register" {
		if len(args) < 2 {
			return fmt.Errorf("session ID required")
		}
		id = args[1]
		remaining = args[2:]
	}
	f := flag.NewFlagSet("session "+action, flag.ContinueOnError)
	f.SetOutput(diagnostic)
	credential := f.String("credential-file", "", "файл capability текущего подключения")
	operation := f.String("operation", "", "устойчивый ID операции; повтор использует тот же ID")
	actor := f.String("as", "", "capability координатора для назначения")
	agent := f.String("agent-id", "", "стабильный ID агента")
	role := f.String("role", "", "coordinator|executor|reviewer|auditor")
	instructions := f.String("instructions", "", "инструкции только этой сессии")
	connection := f.String("connection", "", "уникальный ID отдельного контекста frontend")
	generation := f.Uint64("generation", 0, "ожидаемое текущее поколение подключения")
	peer := f.String("peer", "", "существующая Telepathy-сессия для уведомлений; необязательно")
	after := f.Uint64("after", 0, "позиция чтения")
	seq := f.Uint64("seq", 0, "подтверждённая позиция журнала")
	file := f.String("file", "", "JSON checkpoint")
	stdin := f.Bool("stdin", false, "читать текст или checkpoint из stdin")
	registration := f.String("registration", "", "ID ожидающей назначения сессии")
	regulation := f.String("regulation", "coord-v1", "принятая версия регламента")
	target := f.String("target", "", "целевая сессия для обновления регламента")
	waitSeconds := f.Int("timeout", 30, "ожидание новых событий в секундах (1..60)")
	capabilities := f.String("capabilities", "", "доступные инструменты через запятую")
	blockers := f.String("blockers", "", "препятствия через запятую")
	if err := f.Parse(remaining); err != nil {
		return err
	}
	input := localchat.Request{Operation: *operation, Actor: *actor}
	if (action == "assign" || action == "invite") && *credential != "" {
		auth, err := loadAuth(*credential, "")
		if err != nil {
			return err
		}
		*actor = auth.Token
		input.Actor = auth.Token
	}
	switch action {
	case "register":
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		r, err := client.Call(ctx, "session-register", localchat.Request{AgentID: *agent, Connection: *connection, Operation: *operation})
		if err != nil {
			return err
		}
		if r.Ticket == nil {
			return fmt.Errorf("нет регистрации")
		}
		raw, err := json.MarshalIndent(r.Ticket, "", "  ")
		if err != nil {
			return err
		}
		path := credentialPath(state, r.Ticket.Auth)
		if err = atomicfile.Write(path, raw); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "session=%s state=WAITING_ASSIGNMENT\ncredential-file=%s\n", r.Ticket.Auth.Session, path)
		return err
	case "invite":
		if *actor == "" {
			if err := client.EnableOwner(state); err != nil {
				return err
			}
		}
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		input.Auth.Session = id
		input.Value = *peer
		r, err := client.Call(ctx, "session-invite", input)
		if err != nil {
			return err
		}
		return encode(r.Invitation)
	case "assign":
		if *actor == "" {
			if err := client.EnableOwner(state); err != nil {
				return err
			}
		}
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		input.Room = id
		input.AgentID = *agent
		input.Role = *role
		input.Instructions = *instructions
		input.Registration = *registration
		r, err := client.Call(ctx, "session-assign", input)
		if err != nil {
			return err
		}
		return encode(r.Session)
	case "resume":
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		req := rooms.ResumeRequest{Session: id, Connection: *connection, Expected: *generation, Operation: *operation, AgentID: *agent, Actor: *actor}
		if *credential != "" {
			auth, err := loadAuth(*credential, id)
			if err != nil {
				return err
			}
			req.Token = auth.Token
			provided := false
			f.Visit(func(v *flag.Flag) {
				if v.Name == "generation" {
					provided = true
				}
			})
			if !provided {
				req.Expected = auth.Generation
			}
		}
		if req.Token == "" && req.Actor == "" {
			if err := client.EnableOwner(state); err != nil {
				return err
			}
		}
		input.Resume = req
		input.Value = *peer
		r, err := client.Call(ctx, "session-resume", input)
		if err != nil {
			return err
		}
		if r.Attachment == nil {
			return fmt.Errorf("нет подтверждённого подключения")
		}
		return saveAttachment(state, r.Attachment, out)
	case "context", "say", "ack", "checkpoint", "registration", "ready", "regulate", "wait":
		auth, err := loadAuth(*credential, id)
		if err != nil {
			return err
		}
		input.Auth = auth
	default:
		return fmt.Errorf("неизвестная session-команда")
	}
	switch action {
	case "regulate":
		if f.NArg() != 0 || *target == "" {
			return fmt.Errorf("regulate requires --target session")
		}
		input.Registration = *target
		input.Value = *regulation
		input.Instructions = *instructions
		_, err := client.Call(ctx, "session-regulation", input)
		return err
	case "registration":
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		r, err := client.Call(ctx, "session-registration", input)
		if err != nil {
			return err
		}
		return encode(r.Registration)
	case "ready":
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		input.Readiness = rooms.Readiness{Regulation: *regulation, Capabilities: splitItems(*capabilities), Blockers: splitItems(*blockers)}
		_, err := client.Call(ctx, "session-ready", input)
		return err
	case "context", "wait":
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		input.After = *after
		endpoint := "session-context"
		if action == "wait" {
			endpoint = "session-wait"
			input.WaitSeconds = *waitSeconds
		}
		r, err := client.Call(ctx, endpoint, input)
		if err != nil {
			return err
		}
		return encode(r.Context)
	case "say":
		input.Text = strings.Join(f.Args(), " ")
		if *stdin {
			if f.NArg() != 0 {
				return fmt.Errorf("текст и --stdin несовместимы")
			}
			raw, err := io.ReadAll(io.LimitReader(in, 8193))
			if err != nil {
				return err
			}
			input.Text = string(raw)
		}
		r, err := client.Call(ctx, "session-say", input)
		if err != nil {
			return err
		}
		return encode(r.Event)
	case "ack":
		if f.NArg() != 0 {
			return fmt.Errorf("лишние аргументы")
		}
		input.After = *seq
		_, err := client.Call(ctx, "session-ack", input)
		return err
	case "checkpoint":
		if f.NArg() != 0 || (*file == "" && !*stdin) || (*file != "" && *stdin) {
			return fmt.Errorf("checkpoint требует --file JSON или --stdin")
		}
		source := in
		if *file != "" {
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			source = f
		}
		d := json.NewDecoder(io.LimitReader(source, 2<<20))
		d.DisallowUnknownFields()
		if err := d.Decode(&input.Checkpoint); err != nil {
			return err
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return fmt.Errorf("лишние данные checkpoint")
		}
		_, err := client.Call(ctx, "session-checkpoint", input)
		return err
	}
	return nil
}

func splitItems(s string) []string {
	result := []string{}
	for _, item := range strings.Split(s, ",") {
		if value := strings.TrimSpace(item); value != "" {
			result = append(result, value)
		}
	}
	return result
}
