package agentsession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type CredentialSource interface {
	Token(context.Context) (string, error)
}

type StaticCredential string

func (s StaticCredential) Token(context.Context) (string, error) {
	if s == "" {
		return "", errors.New("registry credential unavailable")
	}
	return string(s), nil
}

type Operation struct {
	Method string
	Path   string
}

type OpenAPIConfig struct {
	BaseURL         string
	Admit           Operation
	Heartbeat       Operation
	Complete        Operation
	Fence           Operation
	Timeout         time.Duration
	IdempotencyHead string
	StatusMap       map[string]Status
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Sleeper interface {
	Sleep(context.Context, time.Duration) error
}

type TimerSleeper struct{}

func (TimerSleeper) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type OpenAPIClient struct {
	Config     OpenAPIConfig
	HTTP       HTTPDoer
	Credential CredentialSource
	Retry      RetryPolicy
	Sleep      Sleeper
	Rand       *rand.Rand
}

func (c *OpenAPIClient) Admit(ctx context.Context, in AdmitRequest) (Session, error) {
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = IdempotencyKey("admit", in.ProjectID, in.AssignmentID, "", 0, 0, in.LeaseTokenDigest)
	}
	var out Session
	err := c.doJSON(ctx, c.Config.Admit, "", in.IdempotencyKey, in, &out)
	out.Status = c.mapStatus(out.Status)
	return out, err
}

func (c *OpenAPIClient) Heartbeat(ctx context.Context, in HeartbeatRequest) (HeartbeatResult, error) {
	if in.Sequence == 0 {
		return HeartbeatResult{}, errors.New("heartbeat sequence must be positive")
	}
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = IdempotencyKey("heartbeat", in.ProjectID, in.AssignmentID, in.SessionID, in.Generation, in.Sequence, in.LeaseTokenDigest)
	}
	var out HeartbeatResult
	err := c.doJSON(ctx, c.Config.Heartbeat, in.SessionID, in.IdempotencyKey, in, &out)
	out.Status = c.mapStatus(out.Status)
	return out, err
}

func (c *OpenAPIClient) Complete(ctx context.Context, in CompleteRequest) error {
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = IdempotencyKey("complete", "", "", in.SessionID, in.Generation, 0, "")
	}
	return c.doJSON(ctx, c.Config.Complete, in.SessionID, in.IdempotencyKey, in, nil)
}

func (c *OpenAPIClient) Fence(ctx context.Context, in FenceRequest) error {
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = IdempotencyKey("fence", "", "", in.SessionID, in.Generation, 0, in.Reason)
	}
	return c.doJSON(ctx, c.Config.Fence, in.SessionID, in.IdempotencyKey, in, nil)
}

func (c *OpenAPIClient) doJSON(ctx context.Context, op Operation, sessionID, key string, body, out any) error {
	if c.HTTP == nil || c.Credential == nil {
		return errors.New("registry client is not configured")
	}
	token, err := c.Credential.Token(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	attempts := c.Retry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	path := strings.ReplaceAll(op.Path, "{sessionId}", sessionID)
	url := strings.TrimRight(c.Config.BaseURL, "/") + path
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, op.Method, url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		header := c.Config.IdempotencyHead
		if header == "" {
			header = "Idempotency-Key"
		}
		req.Header.Set(header, key)
		resp, err := c.HTTP.Do(req)
		if err == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			defer func() { _ = resp.Body.Close() }()
			if out == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				return nil
			}
			dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
			dec.DisallowUnknownFields()
			return dec.Decode(out)
		}
		if err == nil && resp != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			last = fmt.Errorf("registry HTTP status %d", resp.StatusCode)
			if !retryableStatus(resp.StatusCode) {
				return last
			}
		} else {
			last = err
		}
		if attempt == attempts {
			break
		}
		delay := c.retryDelay(attempt)
		if c.Sleep == nil {
			c.Sleep = TimerSleeper{}
		}
		if err := c.Sleep.Sleep(ctx, delay); err != nil {
			return err
		}
	}
	return fmt.Errorf("registry retry exhausted: %w", last)
}

func (c *OpenAPIClient) retryDelay(attempt int) time.Duration {
	base := float64(c.Retry.InitialDelay) * math.Pow(c.Retry.Multiplier, float64(attempt-1))
	if base <= 0 {
		base = float64(100 * time.Millisecond)
	}
	if c.Retry.MaxDelay > 0 && time.Duration(base) > c.Retry.MaxDelay {
		base = float64(c.Retry.MaxDelay)
	}
	if c.Retry.JitterFraction > 0 && c.Rand != nil {
		base += base * c.Retry.JitterFraction * (c.Rand.Float64()*2 - 1)
	}
	return time.Duration(base)
}

func (c *OpenAPIClient) mapStatus(in Status) Status {
	if mapped, ok := c.Config.StatusMap[strings.ToLower(string(in))]; ok {
		return mapped
	}
	return in
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func ParseRetryAfter(value string) (time.Duration, bool) {
	seconds, err := strconv.Atoi(value)
	if err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	return 0, false
}
