// Package agent 运行在内网机器上，主动外连 Relay 拉取任务并执行。
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"relay/internal/protocol"
)

// Config Agent 运行参数。
type Config struct {
	RelayURL string
	HostID   string
	Token    string
	Labels   map[string]string
	CAFile   string
	Insecure bool
	Version  string
	MaxOut   int
}

// Agent 是 Agent 进程主体。
type Agent struct {
	cfg         Config
	client      *http.Client
	pollTimeout time.Duration
}

const defaultMaxOut = 256 << 10

// New 构造 Agent。
func New(cfg Config) (*Agent, error) {
	if cfg.MaxOut <= 0 {
		cfg.MaxOut = defaultMaxOut
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	if cfg.CAFile != "" || cfg.Insecure {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.Insecure}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read ca: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("no valid cert in ca file")
			}
			tlsCfg.RootCAs = pool
		}
		tr.TLSClientConfig = tlsCfg
	}
	return &Agent{
		cfg:         cfg,
		client:      &http.Client{Transport: tr, Timeout: 90 * time.Second},
		pollTimeout: 25 * time.Second,
	}, nil
}

// Run 主循环：连上 Relay，持续拉取并执行任务，断线自动重连。
func (a *Agent) Run(ctx context.Context) error {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := a.handshake(ctx); err != nil {
			log.Printf("handshake failed: %v; retry in %s", err, backoff)
			sleepCtx(ctx, backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		log.Printf("connected to %s as host %q", a.cfg.RelayURL, a.cfg.HostID)
		if err := a.loop(ctx); err != nil && ctx.Err() == nil {
			log.Printf("poll loop broken: %v; reconnecting in %s", err, backoff)
			sleepCtx(ctx, backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}
	return ctx.Err()
}

func (a *Agent) handshake(ctx context.Context) error {
	body := protocol.HandshakeRequest{
		HostID:  a.cfg.HostID,
		Labels:  a.cfg.Labels,
		Version: a.cfg.Version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	var resp protocol.HandshakeResponse
	if err := a.do(ctx, http.MethodPost, "/v1/agent/handshake", body, &resp); err != nil {
		return err
	}
	if resp.PollTimeoutS > 0 {
		a.pollTimeout = time.Duration(resp.PollTimeoutS) * time.Second
	}
	if resp.MaxOutput > 0 {
		a.cfg.MaxOut = resp.MaxOutput
	}
	return nil
}

func (a *Agent) loop(ctx context.Context) error {
	for ctx.Err() == nil {
		task, err := a.poll(ctx)
		if err != nil {
			return err
		}
		if task == nil {
			continue
		}
		log.Printf("task %s argv=%v timeout=%dms", task.ID, task.Args, task.TimeoutMS)
		res := a.execute(task)
		log.Printf("task %s done exit=%d dur=%dms truncated=%v", task.ID, res.ExitCode, res.DurationMS, res.Truncated)
		if err := a.postResult(ctx, res); err != nil {
			log.Printf("post result for %s failed: %v (will retry)", task.ID, err)
			a.postResultWithRetry(ctx, res)
		}
	}
	return ctx.Err()
}

func (a *Agent) poll(ctx context.Context) (*protocol.Task, error) {
	url := fmt.Sprintf("/v1/agent/poll?wait=%d", int(a.pollTimeout/time.Second))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.cfg.RelayURL, "/")+url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var t protocol.Task
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
			return nil, fmt.Errorf("decode task: %w", err)
		}
		return &t, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("auth rejected (%d), check token", resp.StatusCode)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("poll: %d %s", resp.StatusCode, bytes.TrimSpace(b))
	}
}

func (a *Agent) postResult(ctx context.Context, res *protocol.Result) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	return a.do(cctx, http.MethodPost, "/v1/agent/result", res, nil)
}

func (a *Agent) postResultWithRetry(ctx context.Context, res *protocol.Result) {
	for i := 0; i < 3 && ctx.Err() == nil; i++ {
		sleepCtx(ctx, time.Duration(i+1)*2*time.Second)
		if err := a.postResult(ctx, res); err == nil {
			return
		}
	}
}

func (a *Agent) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.cfg.RelayURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Check 只做一次握手验证连通性和凭据，然后退出。
func (a *Agent) Check(ctx context.Context) error {
	if err := a.handshake(ctx); err != nil {
		return err
	}
	fmt.Printf("OK: relay=%s host=%s\n", a.cfg.RelayURL, a.cfg.HostID)
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// capWriter 只保留前 limit 字节，超出的丢弃并置 truncated。
type capWriter struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	remain := w.limit - w.buf.Len()
	if remain <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		w.buf.Write(p[:remain])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *capWriter) String() string { return w.buf.String() }
