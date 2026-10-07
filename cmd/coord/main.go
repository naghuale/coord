//go:build unix

// coord connects an existing Telepathy agent to a local conversation adapter.
package main

import (
	"bufio"
	"context"
	"coord/checkpoint"
	"coord/internal/localchat"
	"coord/telepathy"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed bootstrap.md
var bootstrapPrompt string

const usage = `Coord — совместная работа существующих AI-агентов по задачам.

  coord start PROJECT --coordinator PEER --agent-id ID --operation ID
                                       owner приглашает координатора одной командой
  coord prompt                         общий стартовый промпт AI-агента
  coord sessions                       логические сессии, роли и поколения
  coord session assign <room> --agent-id ID --role ROLE --instructions TEXT
  coord session invite <session> --operation ID [--peer PEER]
  coord session register --agent-id ID --connection ID --operation ID
  coord session registration <session> --credential-file FILE
  coord session resume <session> --connection ID --operation ID [--credential-file FILE]
  coord session context <session> --credential-file FILE [--after SEQ]
  coord session wait <session> --credential-file FILE --after SEQ --timeout 30
  coord session ready <session> --credential-file FILE --operation ID --regulation coord-v1
  coord session say <session> --credential-file FILE --operation ID TEXT
  coord session ack <session> --credential-file FILE --operation ID --seq SEQ
  coord session checkpoint <session> --credential-file FILE --operation ID --file JSON
  coord history status                 cursor, commit, backlog и ошибки
  coord history export [--content]      локальный Git-экспорт; content только явно
  coord room create <задача>             создать отдельную комнату задачи
  coord rooms                          список комнат
  coord invite <room> <агент> --role executor|coordinator|reviewer|auditor
  coord watch <room>                    наблюдать общий журнал
  coord join <room>                     войти как owner и общаться
  coord say <room> "реплика"             написать как owner
  coord log <room>                      прочитать историю
  coord close <room>                    закрыть комнату

Дополнительная диагностика прямого подключения:

  coord peers                 показать доступные сессии
  coord connect <агент>        пригласить агента и дождаться подтверждения
  coord chat                  начать беседу (/quit — выход)
  coord chat "сообщение"       отправить одну реплику
  coord status                показать подключение и последний результат
  coord stop                  остановить локальный адаптер

Агент получает команды accept/reply автоматически в приглашении.
Локальный адаптер запускается сам. Другой AI-процесс не запускается.
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, in io.Reader, out, diagnostic io.Writer) error {
	flags := flag.NewFlagSet("coord", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	stateFlag := flags.String("state", filepath.Join("state", "coord"), "локальная папка состояния")
	serverFlag := flags.String("server", "", "путь к Telepathy 0.8.0 server.mjs (обычно определяется автоматически)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprint(out, usage)
			return err
		}
		return err
	}
	positional := flags.Args()
	if len(positional) == 0 {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	command := positional[0]
	values := positional[1:]
	if command == "prompt" {
		if len(values) != 0 {
			return fmt.Errorf("coord prompt")
		}
		_, err := fmt.Fprint(out, bootstrapPrompt)
		return err
	}
	if command == "help" {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	state, err := filepath.Abs(*stateFlag)
	if err != nil {
		return err
	}
	if command == "_serve" {
		return serve(ctx, state, *serverFlag)
	}
	if roomCommand(command) {
		return runRooms(ctx, command, values, state, *serverFlag, in, out, diagnostic)
	}
	valid := map[string]bool{"peers": true, "connect": true, "chat": true, "status": true, "stop": true, "accept": true, "reply": true}
	if !valid[command] {
		return fmt.Errorf("неизвестная команда %q; coord help", command)
	}
	if (command == "connect" || command == "accept") && len(values) != 1 {
		return fmt.Errorf("coord %s требует один аргумент", command)
	}
	if command == "reply" && len(values) < 2 {
		return fmt.Errorf("coord reply <ticket> <ответ> или coord reply <ticket> --stdin")
	}
	if (command == "peers" || command == "status" || command == "stop") && len(values) != 0 {
		return fmt.Errorf("coord %s не принимает аргументы", command)
	}
	client := localchat.NewClient(state)
	defer client.Close()
	if command == "peers" || command == "connect" || command == "chat" {
		if err = ensure(ctx, state, *serverFlag, client); err != nil {
			return err
		}
	}
	if command != "accept" && command != "reply" {
		if err = client.EnableOwner(state); err != nil {
			if localchat.Offline(err) && (command == "status" || command == "stop") {
				_, err = fmt.Fprintln(out, "Coord остановлен.")
				return err
			}
			return err
		}
	}
	switch command {
	case "peers":
		result, err := client.Call(ctx, "peers", localchat.Request{})
		if err != nil {
			return err
		}
		if len(result.Peers) == 0 {
			_, err = fmt.Fprintln(out, "Доступных агентов нет.")
			return err
		}
		for _, p := range result.Peers {
			if _, err = fmt.Fprintf(out, "%s [%s] — %s\n", p.Address, p.Ref, p.Activity); err != nil {
				return err
			}
		}
	case "connect":
		if _, err = fmt.Fprintln(diagnostic, "Приглашение агенту. Ожидаю подтверждения Coord…"); err != nil {
			return err
		}
		result, err := client.Call(ctx, "connect", localchat.Request{Value: values[0]})
		if err != nil {
			return err
		}
		if result.Peer == nil {
			return fmt.Errorf("нет подтверждения агента")
		}
		_, err = fmt.Fprintf(out, "Подключён: %s [%s]. Теперь coord chat\n", result.Peer.Address, result.Peer.Ref)
		return err
	case "accept":
		var accepted localchat.Response
		if accepted, err = client.Call(ctx, "accept", localchat.Request{Value: values[0]}); err != nil {
			return err
		}
		if accepted.Attachment != nil {
			return saveAttachment(state, accepted.Attachment, out)
		}
		_, err = fmt.Fprintln(out, "Подключение Coord подтверждено.")
		return err
	case "reply":
		text := strings.Join(values[1:], " ")
		if len(values) == 2 && values[1] == "--stdin" {
			b, e := io.ReadAll(io.LimitReader(in, (32<<10)+1))
			if e != nil {
				return e
			}
			text = string(b)
		}
		if _, err = client.Call(ctx, "reply", localchat.Request{Value: values[0], Text: text}); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Ответ принят локальным адаптером. Завершение проверяется через coord status.")
		return err
	case "chat":
		send := func(text string) error {
			result, err := client.Call(ctx, "chat", localchat.Request{Text: text})
			if err != nil {
				return err
			}
			if result.Answer == nil {
				return fmt.Errorf("ответ не подтверждён")
			}
			_, err = fmt.Fprintln(out, result.Answer.Text)
			return err
		}
		if len(values) > 0 {
			return send(strings.Join(values, " "))
		}
		status, err := client.Call(ctx, "status", localchat.Request{})
		if err != nil {
			return err
		}
		if status.Status == nil || status.Status.Peer == nil || !status.Status.Connected {
			return localchat.ErrNotConnected
		}
		if _, err = fmt.Fprintf(out, "Беседа с %s. /quit — выход.\n", status.Status.Peer.Address); err != nil {
			return err
		}
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), 16<<10)
		for {
			if _, err = fmt.Fprint(out, "> "); err != nil {
				return err
			}
			if !scanner.Scan() {
				return scanner.Err()
			}
			text := strings.TrimSpace(scanner.Text())
			if text == "/quit" {
				return nil
			}
			if text == "" {
				continue
			}
			if err = send(text); err != nil {
				return err
			}
		}
	case "status":
		result, err := client.Call(ctx, "status", localchat.Request{})
		if err != nil {
			if localchat.Offline(err) {
				_, err = fmt.Fprintln(out, "Coord остановлен.")
				return err
			}
			return err
		}
		if result.Status == nil {
			return fmt.Errorf("нет состояния")
		}
		st := result.Status
		if st.Peer == nil {
			if _, err = fmt.Fprintln(out, "Агент не подключён."); err != nil {
				return err
			}
		} else {
			if _, err = fmt.Fprintf(out, "Агент: %s [%s]\n", st.Peer.Address, st.Peer.Ref); err != nil {
				return err
			}
		}
		if st.Last != nil {
			receipt := "не подтверждён"
			if st.Last.ReceiptCommitted {
				receipt = "подтверждён"
			}
			if _, err = fmt.Fprintf(out, "Последняя реплика: %s; RESULT_ACK %s\n", st.Last.Status, receipt); err != nil {
				return err
			}
			if !st.Waiting && st.Last.Status != "DONE" && st.Last.Status != "FAILED" {
				_, err = fmt.Fprintln(out, "Реплика прервана. Автоматический повтор не выполнялся; отправьте новый вопрос явно.")
				return err
			}
		}
	case "stop":
		if _, err = client.Call(ctx, "stop", localchat.Request{}); err != nil {
			if !localchat.Offline(err) {
				return err
			}
		}
		_, err = fmt.Fprintln(out, "Coord остановлен.")
		return err
	}
	return nil
}
func ensure(ctx context.Context, state, server string, client *localchat.Client) error {
	if _, err := client.Call(ctx, "health", localchat.Request{}); err == nil {
		return nil
	} else if !localchat.Offline(err) {
		return err
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(state, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	args := []string{"--state", state}
	if server != "" {
		args = append(args, "--server", server)
	}
	args = append(args, "_serve")
	child := exec.Command(exe, args...)
	child.Stdout = log
	child.Stderr = log
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = child.Start(); err != nil {
		return errors.Join(err, log.Close())
	}
	if err = errors.Join(child.Process.Release(), log.Close()); err != nil {
		return err
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := client.Call(ctx, "health", localchat.Request{}); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("локальный Coord не запустился; проверьте %s и TELEPATHY_SERVER", filepath.Join(state, "daemon.log"))
		case <-poll.C:
		}
	}
}
func discover(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if path := os.Getenv("TELEPATHY_SERVER"); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".codex", "plugins", "cache", "telepathy", "telepathy", "0.8.0", "dist", "server.mjs")
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path, nil
	}
	return "", fmt.Errorf("telepathy 0.8.0 не найден; задайте TELEPATHY_SERVER или --server")
}
func serve(ctx context.Context, state, explicitServer string) (err error) {
	if err = os.MkdirAll(state, 0700); err != nil {
		return err
	}
	release, err := (checkpoint.File{Path: filepath.Join(state, "daemon")}).Lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tools, closeTools, err := openTools(ctx, state, explicitServer)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeTools()) }()
	service, err := localchat.New(localchat.Config{State: state, Executable: exe, Now: time.Now}, tools)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, service.Close()) }()
	socket := localchat.SocketPath(state)
	if len(socket) >= 100 {
		return fmt.Errorf("слишком длинный путь Unix socket; используйте более короткий --state")
	}
	// Ownership lock is held before replacing an orphaned socket.
	if info, e := os.Lstat(socket); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refuse replacing non-socket %s", socket)
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	httpServer := &http.Server{Handler: service.Handler(cancel), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Cancel model waits before draining handlers and closing canonical storage.
		closeErr := service.Close()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		shutdownErr := httpServer.Shutdown(cleanup)
		if shutdownErr != nil {
			shutdownErr = errors.Join(shutdownErr, httpServer.Close())
		}
		serveErr := <-done
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(closeErr, shutdownErr, serveErr)
	}
}

type localTools struct{}

func (localTools) Call(_ context.Context, method string, _ map[string]any) (string, error) {
	if method == "list_peers" {
		return "Local API mode; no Telepathy peers.", nil
	}
	return "", fmt.Errorf("telepathy disabled; local session API remains available")
}
func openTools(ctx context.Context, state, explicit string) (localchat.Tools, func() error, error) {
	if explicit == "none" {
		return localTools{}, func() error { return nil }, nil
	}
	server, err := discover(explicit)
	if err != nil {
		return nil, nil, err
	}
	if !filepath.IsAbs(server) {
		server, err = filepath.Abs(server)
		if err != nil {
			return nil, nil, err
		}
	}
	env := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TELEPATHY_AGENT_PID=") {
			env = append(env, value)
		}
	}
	env = append(env, fmt.Sprintf("TELEPATHY_AGENT_PID=%d", os.Getpid()))
	process, err := telepathy.Start(ctx, []string{"node", server, "--agent", "opencode"}, state, env)
	if err != nil {
		return nil, nil, err
	}
	return process, process.Close, nil
}
