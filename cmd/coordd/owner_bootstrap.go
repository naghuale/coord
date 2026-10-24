package main

import (
	"bufio"
	"bytes"
	"coord/checkpoint"
	"coord/envelope"
	"coord/ownercredential"
	"coord/store"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxBootstrapCredentialFile = 64 << 10

type bootstrapCredentialInput struct {
	ID            string `json:"id"`
	COSEPublicKey []byte `json:"cose_public_key"`
}

// runOwnerCommand performs the explicit one-time TOFU ceremony. The input file
// contains public COSE keys only; no private authenticator material is read.
func runOwnerCommand(args []string, in io.Reader, out, diagnostic io.Writer) error {
	if len(args) == 0 || args[0] != "bootstrap" {
		return errors.New("usage: coordd owner bootstrap --state-dir DIR --principal ID --credentials FILE")
	}
	flags := flag.NewFlagSet("coordd owner bootstrap", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	stateDir := flags.String("state-dir", "", "canonical coordd state directory")
	principal := flags.String("principal", "", "stable Owner principal ID")
	credentialsPath := flags.String("credentials", "", "JSON file containing at least two public COSE credentials")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if len(flags.Args()) != 0 || *stateDir == "" || *principal == "" || *credentialsPath == "" || strings.TrimSpace(*principal) != *principal {
		return errors.New("usage: coordd owner bootstrap --state-dir DIR --principal ID --credentials FILE")
	}
	file, err := os.Open(*credentialsPath)
	if err != nil {
		return fmt.Errorf("open public credential file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			_, _ = fmt.Fprintf(diagnostic, "close public credential file: %v\n", closeErr)
		}
	}()
	data, err := io.ReadAll(io.LimitReader(file, maxBootstrapCredentialFile+1))
	if err != nil {
		return fmt.Errorf("read public credential file: %w", err)
	}
	if len(data) > maxBootstrapCredentialFile {
		return errors.New("public credential file exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input []bootstrapCredentialInput
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("decode public credentials: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("public credential file must contain one JSON value")
	}
	if len(input) < 2 {
		return errors.New("bootstrap requires at least two public credentials")
	}
	credentials := make([]ownercredential.Credential, 0, len(input))
	now := time.Now().UTC()
	for _, item := range input {
		credential, err := ownercredential.CredentialFromCOSE(item.ID, item.COSEPublicKey, now)
		if err != nil {
			return fmt.Errorf("credential %q: %w", item.ID, err)
		}
		credentials = append(credentials, credential)
	}
	_, _ = fmt.Fprintln(out, "Initial Owner credential bootstrap (TOFU). Verify these fingerprints against the physical authenticators:")
	for _, credential := range credentials {
		_, _ = fmt.Fprintf(out, "  %s %s\n", credential.ID, credential.Fingerprint)
	}
	_, _ = fmt.Fprintf(out, "Type exactly BOOTSTRAP %s to initialize this canonical state: ", *principal)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSuffix(line, "\n") != "BOOTSTRAP "+*principal {
		return errors.New("owner credential bootstrap confirmation did not match")
	}
	root, err := filepath.Abs(*stateDir)
	if err != nil {
		return err
	}
	canonical, err := store.NewCanonical(checkpoint.CanonicalFile{Path: filepath.Join(root, "canonical.json")})
	if err != nil {
		return fmt.Errorf("open canonical state for bootstrap: %w", err)
	}
	defer func() {
		if closeErr := canonical.Close(); closeErr != nil {
			_, _ = fmt.Fprintf(diagnostic, "close canonical store: %v\n", closeErr)
		}
	}()
	if _, err := canonical.BootstrapOwnerCredentials(*principal, envelope.NewID(), credentials); err != nil {
		return fmt.Errorf("bootstrap Owner credentials: %w", err)
	}
	_, _ = fmt.Fprintln(out, "Owner credentials initialized in canonical.json")
	return nil
}
