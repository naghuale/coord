package main

import (
	"context"
	"coord/envelope"
	"coord/watchdog"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	url := flag.String("coordinator", "http://127.0.0.1:8090", "control API URL")
	fence := flag.String("fence-command", "", "executable that stops old writer and releases lock")
	spawn := flag.String("spawn-command", "", "executable that prints replacement session")
	stop := flag.String("stop-command", "", "executable that stops replacement session")
	interval := flag.Duration("interval", 10*time.Second, "poll interval")
	flag.Parse()
	token := os.Getenv("COORD_CONTROL_TOKEN")
	if token == "" || *fence == "" || *spawn == "" || *stop == "" {
		log.Fatal("COORD_CONTROL_TOKEN and fence/spawn/stop commands are required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	control := watchdog.HTTPControl{URL: *url, Token: token, ClaimID: envelope.NewID(), Client: &http.Client{Timeout: 30 * time.Second}}
	backend := watchdog.CommandBackend{Control: control, FenceCommand: []string{*fence}, SpawnCommand: []string{*spawn}, StopCommand: []string{*stop}}
	w := watchdog.Watchdog{Backend: backend, Interval: *interval, OnError: func(err error) { log.Printf("watchdog sweep: %v", err) }}
	if err := w.Loop(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
