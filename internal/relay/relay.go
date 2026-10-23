// Package relay 是中转服务：维护内网机器的长轮询连接队列、任务状态与审计。
package relay

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"relay/internal/policy"
	"relay/internal/protocol"
)

// Config Relay 运行参数。
type Config struct {
	Addr        string
	APIToken    string
	AgentTokens map[string]string // token -> host id
	Policy      *policy.Config
	AuditPath   string
	MaxOutput   int
	PollTimeout time.Duration

	// 下载端点：GET /dl/{name}?k=<token>，用于把安装包直接发到内网机器
	DownloadDir   string
	DownloadToken string
}

// Server 实现 Relay 的 HTTP 接口。
type Server struct {
	cfg Config

	mu    sync.Mutex
	hosts map[string]*host
	tasks map[string]*protocol.TaskView
	audit *os.File
}

type host struct {
	info     protocol.HostInfo
	q        chan *protocol.Task
	lastSeen time.Time
}

// New 构造 Relay。
func New(cfg Config) (*Server, error) {
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = 25 * time.Second
	}
	if cfg.MaxOutput <= 0 {
		cfg.MaxOutput = 256 << 10
	}
	s := &Server{
		cfg:   cfg,
		hosts: map[string]*host{},
		tasks: map[string]*protocol.TaskView{},
	}
	if cfg.AuditPath != "" {
		f, err := os.OpenFile(cfg.AuditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if err != nil {
			return nil, fmt.Errorf("open audit: %w", err)
		}
		s.audit = f
	}
	return s, nil
}

// Close 关闭审计文件。
func (s *Server) Close() error {
	if s.audit != nil {
		return s.audit.Close()
	}
	return nil
}

// Handler 返回注册好路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.Handle("POST /v1/agent/handshake", s.agentAuth(http.HandlerFunc(s.handleHandshake)))
	mux.Handle("GET /v1/agent/poll", s.agentAuth(http.HandlerFunc(s.handlePoll)))
	mux.Handle("POST /v1/agent/result", s.agentAuth(http.HandlerFunc(s.handleResult)))

	mux.Handle("GET /v1/hosts", s.apiAuth(http.HandlerFunc(s.handleHosts)))
	mux.Handle("POST /v1/tasks", s.apiAuth(http.HandlerFunc(s.handleCreateTask)))
	mux.Handle("GET /v1/tasks/{id}", s.apiAuth(http.HandlerFunc(s.handleGetTask)))
	mux.HandleFunc("GET /dl/{name}", s.handleDownload)
	return logRequests(mux)
}

// ---------------------------------------------------------------- 中间件

type ctxKey string

const hostIDKey ctxKey = "host_id"

func (s *Server) agentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		hostID, ok := lookupToken(s.cfg.AgentTokens, tok)
		if !ok {
			writeErr(w, 401, "invalid agent token")
			return
		}
		next.ServeHTTP(w, r.WithContext(withHost(r.Context(), hostID)))
	})
}

func (s *Server) apiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken == "" || !constEq(bearer(r), s.cfg.APIToken) {
			writeErr(w, 401, "invalid api token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			log.Printf("%s %s from=%s took=%s", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start).Round(time.Millisecond))
		}
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func lookupToken(m map[string]string, tok string) (string, bool) {
	if tok == "" {
		return "", false
	}
	for t, host := range m {
		if constEq(t, tok) {
			return host, true
		}
	}
	return "", false
}

func constEq(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---------------------------------------------------------------- Agent 接口

func (s *Server) handleHandshake(w http.ResponseWriter, r *http.Request) {
	hostID := hostFrom(r)
	var req protocol.HandshakeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.HostID != "" && req.HostID != hostID {
		writeErr(w, 403, "host id mismatch")
		return
	}

	s.mu.Lock()
	h, ok := s.hosts[hostID]
	if !ok {
		h = &host{q: make(chan *protocol.Task, 256)}
		s.hosts[hostID] = h
	}
	h.info.ID = hostID
	h.info.Labels = req.Labels
	h.info.Version = req.Version
	h.info.Online = true
	h.info.LastSeen = time.Now()
	h.lastSeen = time.Now()
	s.mu.Unlock()

	log.Printf("agent online: host=%s os=%s/%s version=%s labels=%v", hostID, req.OS, req.Arch, req.Version, req.Labels)
	writeJSON(w, 200, protocol.HandshakeResponse{
		PollTimeoutS: int(s.cfg.PollTimeout / time.Second),
		MaxOutput:    s.cfg.MaxOutput,
		HostID:       hostID,
	})
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	hostID := hostFrom(r)
	s.mu.Lock()
	h, ok := s.hosts[hostID]
	if !ok {
		s.mu.Unlock()
		writeErr(w, 404, "unknown host, handshake first")
		return
	}
	h.lastSeen = time.Now()
	h.info.LastSeen = time.Now()
	h.info.Online = true
	s.mu.Unlock()

	timeout := s.cfg.PollTimeout
	if v := r.URL.Query().Get("wait"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d > 0 && d <= 60*time.Second {
			timeout = d
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case task := <-h.q:
		s.mu.Lock()
		if tv, ok := s.tasks[task.ID]; ok {
			tv.State = protocol.StateAssigned
		}
		s.mu.Unlock()
		writeJSON(w, 200, task)
	case <-timer.C:
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
	}
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	hostID := hostFrom(r)
	var res protocol.Result
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(s.cfg.MaxOutput*2+64<<10))).Decode(&res); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	s.mu.Lock()
	tv, ok := s.tasks[res.TaskID]
	if ok {
		tv.Result = &res
		if res.Err != "" {
			tv.State = protocol.StateError
		} else {
			tv.State = protocol.StateDone
		}
	}
	s.mu.Unlock()

	s.writeAudit(map[string]any{
		"ts":      time.Now().UTC(),
		"event":   "result",
		"host":    hostID,
		"task_id": res.TaskID,
		"exit":    res.ExitCode,
		"err":     res.Err,
		"dur_ms":  res.DurationMS,
	})
	log.Printf("result host=%s task=%s exit=%d dur=%dms err=%q", hostID, res.TaskID, res.ExitCode, res.DurationMS, res.Err)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------- 控制面接口

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]protocol.HostInfo, 0, len(s.hosts))
	for _, h := range s.hosts {
		info := h.info
		info.Online = time.Since(h.lastSeen) < 2*s.cfg.PollTimeout+10*time.Second
		info.Pending = len(h.q)
		info.LastSeen = h.lastSeen
		out = append(out, info)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateTaskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.HostID == "" || len(req.Args) == 0 {
		writeErr(w, 400, "host_id and args are required")
		return
	}
	if req.TimeoutMS <= 0 {
		req.TimeoutMS = 30000
	}
	if req.TimeoutMS > 600000 {
		writeErr(w, 400, "timeout_ms too large (max 600000)")
		return
	}
	if err := s.cfg.Policy.Check(req.HostID, req.Args); err != nil {
		writeErr(w, 403, err.Error())
		return
	}

	s.mu.Lock()
	h, ok := s.hosts[req.HostID]
	if !ok {
		s.mu.Unlock()
		writeErr(w, 404, "unknown host: "+req.HostID)
		return
	}
	// 幂等：同一 idem 已存在则直接返回旧任务
	if req.Idem != "" {
		for _, tv := range s.tasks {
			if tv.Task.Idem == req.Idem && tv.Task.HostID == req.HostID {
				s.mu.Unlock()
				writeJSON(w, 200, tv)
				return
			}
		}
	}
	task := &protocol.Task{
		ID:        newID(),
		HostID:    req.HostID,
		Kind:      protocol.KindExec,
		Args:      req.Args,
		Cwd:       req.Cwd,
		Env:       req.Env,
		TimeoutMS: req.TimeoutMS,
		Idem:      req.Idem,
		CreatedAt: time.Now().UTC(),
	}
	select {
	case h.q <- task:
	default:
		s.mu.Unlock()
		writeErr(w, 429, "host queue full, try later")
		return
	}
	tv := &protocol.TaskView{Task: *task, State: protocol.StatePending}
	s.tasks[task.ID] = tv
	s.mu.Unlock()

	s.writeAudit(map[string]any{
		"ts":      time.Now().UTC(),
		"event":   "task",
		"host":    req.HostID,
		"task_id": task.ID,
		"argv":    req.Args,
	})
	log.Printf("task queued host=%s task=%s argv=%v", req.HostID, task.ID, req.Args)
	writeJSON(w, 202, tv)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	tv, ok := s.tasks[id]
	s.mu.Unlock()
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	writeJSON(w, 200, tv)
}

// handleDownload 提供受令牌保护的静态文件下载，方便内网机器直接用浏览器取安装包。
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DownloadToken == "" || r.URL.Query().Get("k") != s.cfg.DownloadToken {
		writeErr(w, 403, "forbidden")
		return
	}
	name := filepath.Base(r.PathValue("name"))
	if name == "." || name == "/" || strings.HasPrefix(name, ".") {
		writeErr(w, 404, "not found")
		return
	}
	full := filepath.Join(s.cfg.DownloadDir, name)
	f, err := os.Open(full)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeErr(w, 404, "not found")
		return
	}
	log.Printf("download %s from=%s", name, r.RemoteAddr)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// ---------------------------------------------------------------- 工具

func (s *Server) writeAudit(rec map[string]any) {
	if s.audit == nil {
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b = append(b, '\n')
	s.mu.Lock()
	_, _ = s.audit.Write(b)
	s.mu.Unlock()
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "t_" + hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, protocol.ErrorResponse{Error: msg})
}
