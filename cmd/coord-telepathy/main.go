//go:build unix

// coord-telepathy runs one independently configured signed Telepathy host or worker.
package main

import (
	"context"
	"coord/internal/telepathyhost"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	config := flag.String("config", "", "absolute path to private host/worker JSON configuration")
	keygen := flag.Bool("generate-key", false, "print a fresh Ed25519 keypair as JSON; store private output securely")
	flag.Parse()
	if *keygen {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"public_key": base64.StdEncoding.EncodeToString(public), "private_key": base64.StdEncoding.EncodeToString(private)}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *config == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: coord-telepathy -config /absolute/path/config.json")
		os.Exit(2)
	}
	cfg, err := telepathyhost.Load(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result, err := telepathyhost.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if result.Status == "FAILED" {
		os.Exit(1)
	}
}
