// Package mcp 把 Relay 的控制面 API 暴露成 MCP stdio server，供大模型调用。
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"relay/internal/protocol"
)

// Config MCP server 参数。
type Config struct {
	APIURL   string
	Token    string
	CAFile   string
	Insecure bool
	Version  string
}

// Server 是 MCP stdio server。
type Server struct {
	cfg    Config
	client *http.Client
}

// New 构造 MCP server。
func New(cfg Config) *Server {
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	if cfg.CAFile != "" || cfg.Insecure {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.Insecure}
		if cfg.CAFile != "" {
			if pemBytes, err := os.ReadFile(cfg.CAFile); err == nil {
				pool := x509.NewCertPool()
				pool.AppendCertsFromPEM(pemBytes)
				tlsCfg.RootCAs = pool
			}
		}
		tr.TLSClientConfig = tlsCfg
	}
	return &Server{cfg: cfg, client: &http.Client{Transport: tr, Timeout: 120 * time.Second}}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve 在 in/out 上跑 JSON-RPC 循环，直到 EOF。
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	enc := json.NewEncoder(out)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
			continue
		}
		// 通知类消息（无 id）不需要应答
		if len(req.ID) == 0 {
			continue
		}
		resp := s.dispatch(&req)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) dispatch(req *rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "intranet-relay", "version": s.cfg.Version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": toolDefs()}
	case "tools/call":
		res, err := s.callTool(req.Params)
		if err != nil {
			resp.Result = toolResult("错误: "+err.Error(), true)
		} else {
			resp.Result = res
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func toolDefs() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_hosts",
			"description": "列出所有已注册的内网机器及其在线状态、标签、待执行任务数。",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "exec",
			"description": "在内网机器上执行一条命令（不经过 shell，必须传 argv 数组）。受 Relay 侧白名单策略约束。返回退出码、stdout、stderr。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"host":       map[string]any{"type": "string", "description": "目标主机 ID"},
					"argv":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "命令及参数，例如 [\"tail\",\"-n\",\"200\",\"/var/log/app.log\"]"},
					"timeout_ms": map[string]any{"type": "integer", "description": "超时毫秒数，默认 30000"},
					"cwd":        map[string]any{"type": "string", "description": "工作目录，可省略"},
				},
				"required": []string{"host", "argv"},
			},
		},
		{
			"name":        "tail_log",
			"description": "查看内网机器上的日志：给 path 则 tail 文件，给 unit 则 journalctl。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"host":  map[string]any{"type": "string"},
					"path":  map[string]any{"type": "string", "description": "日志文件路径，与 unit 二选一"},
					"unit":  map[string]any{"type": "string", "description": "systemd 服务名，与 path 二选一"},
					"lines": map[string]any{"type": "integer", "description": "行数，默认 200"},
				},
				"required": []string{"host"},
			},
		},
		{
			"name":        "get_task",
			"description": "按 task_id 查询一次异步任务的执行结果。",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"task_id": map[string]any{"type": "string"}},
				"required":   []string{"task_id"},
			},
		},
	}
}

func (s *Server) callTool(raw json.RawMessage) (map[string]any, error) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	switch params.Name {
	case "list_hosts":
		var hosts []protocol.HostInfo
		if err := s.api(ctx, http.MethodGet, "/v1/hosts", nil, &hosts); err != nil {
			return nil, err
		}
		if len(hosts) == 0 {
			return toolResult("还没有任何机器注册。", false), nil
		}
		var b strings.Builder
		for _, h := range hosts {
			state := "离线"
			if h.Online {
				state = "在线"
			}
			fmt.Fprintf(&b, "- %s [%s] 版本=%s 待执行=%d 标签=%v\n", h.ID, state, h.Version, h.Pending, h.Labels)
		}
		return toolResult(b.String(), false), nil

	case "exec":
		var a struct {
			Host      string   `json:"host"`
			Argv      []string `json:"argv"`
			TimeoutMS int      `json:"timeout_ms"`
			Cwd       string   `json:"cwd"`
		}
		if err := json.Unmarshal(params.Arguments, &a); err != nil {
			return nil, err
		}
		if len(a.Argv) == 0 {
			return nil, errors.New("argv 不能为空")
		}
		text, err := s.runTask(ctx, protocol.CreateTaskRequest{
			HostID: a.Host, Args: a.Argv, TimeoutMS: a.TimeoutMS, Cwd: a.Cwd,
		}, 3*time.Second)
		if err != nil {
			return nil, err
		}
		return toolResult(text, false), nil

	case "tail_log":
		var a struct {
			Host  string `json:"host"`
			Path  string `json:"path"`
			Unit  string `json:"unit"`
			Lines int    `json:"lines"`
		}
		if err := json.Unmarshal(params.Arguments, &a); err != nil {
			return nil, err
		}
		if a.Lines <= 0 {
			a.Lines = 200
		}
		var argv []string
		switch {
		case a.Path != "":
			argv = []string{"tail", "-n", fmt.Sprint(a.Lines), a.Path}
		case a.Unit != "":
			argv = []string{"journalctl", "-u", a.Unit, "-n", fmt.Sprint(a.Lines), "--no-pager"}
		default:
			return nil, errors.New("path 和 unit 至少要给一个")
		}
		text, err := s.runTask(ctx, protocol.CreateTaskRequest{HostID: a.Host, Args: argv}, 3*time.Second)
		if err != nil {
			return nil, err
		}
		return toolResult(text, false), nil

	case "get_task":
		var a struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(params.Arguments, &a); err != nil {
			return nil, err
		}
		var tv protocol.TaskView
		if err := s.api(ctx, http.MethodGet, "/v1/tasks/"+a.TaskID, nil, &tv); err != nil {
			return nil, err
		}
		return toolResult(formatResult(&tv), false), nil
	}
	return nil, fmt.Errorf("未知工具: %s", params.Name)
}

// runTask 创建任务并等待结果（同步语义，适合对话场景）。
func (s *Server) runTask(ctx context.Context, req protocol.CreateTaskRequest, pollEvery time.Duration) (string, error) {
	start := time.Now()
	var tv protocol.TaskView
	if err := s.api(ctx, http.MethodPost, "/v1/tasks", req, &tv); err != nil {
		return "", err
	}
	id := tv.Task.ID
	wait := time.Duration(req.TimeoutMS) * time.Millisecond
	if wait <= 0 {
		wait = 30 * time.Second
	}
	deadline := time.Now().Add(wait + 20*time.Second)

	for time.Now().Before(deadline) {
		if tv.State == protocol.StateDone || tv.State == protocol.StateError {
			return formatResult(&tv) + fmt.Sprintf("\n(耗时 %.2fs)", time.Since(start).Seconds()), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollEvery):
		}
		if err := s.api(ctx, http.MethodGet, "/v1/tasks/"+id, nil, &tv); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("任务 %s 已下发但尚未返回结果（主机可能离线）。稍后用 get_task 查询。", id), nil
}

func formatResult(tv *protocol.TaskView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "主机: %s\n任务: %s\n状态: %s\n", tv.Task.HostID, tv.Task.ID, tv.State)
	if r := tv.Result; r != nil {
		fmt.Fprintf(&b, "退出码: %d\n", r.ExitCode)
		if r.Err != "" {
			fmt.Fprintf(&b, "错误: %s\n", r.Err)
		}
		if r.Truncated {
			b.WriteString("注意: 输出已被截断\n")
		}
		if r.Stdout != "" {
			fmt.Fprintf(&b, "--- stdout ---\n%s\n", r.Stdout)
		}
		if r.Stderr != "" {
			fmt.Fprintf(&b, "--- stderr ---\n%s\n", r.Stderr)
		}
	}
	return b.String()
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

func (s *Server) api(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.cfg.APIURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("relay %s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
	return nil
}
