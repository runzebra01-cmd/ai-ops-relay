// Package protocol 定义 Relay 与 Agent 之间的消息格式，以及控制面 API 的类型。
package protocol

import "time"

// Kind 任务类型。P0 只有 exec。
type Kind string

const (
	KindExec    Kind = "exec"
	KindReadLog Kind = "readlog"
)

// Task 一条下发到内网机器的指令。
type Task struct {
	ID        string            `json:"id"`
	HostID    string            `json:"host_id"`
	Kind      Kind              `json:"kind"`
	Args      []string          `json:"args"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int               `json:"timeout_ms"`
	Idem      string            `json:"idem,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// Result 执行结果。Stdout/Stderr 超限时被截断并置 Truncated。
type Result struct {
	TaskID     string    `json:"task_id"`
	HostID     string    `json:"host_id"`
	ExitCode   int       `json:"exit_code"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	Truncated  bool      `json:"truncated"`
	Err        string    `json:"err,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DurationMS int64     `json:"duration_ms"`
}

// TaskState 任务在 Relay 侧的状态。
type TaskState string

const (
	StatePending  TaskState = "pending"
	StateAssigned TaskState = "assigned"
	StateDone     TaskState = "done"
	StateError    TaskState = "error"
	StateCanceled TaskState = "canceled"
)

// TaskView 控制面查询任务时返回的视图。
type TaskView struct {
	Task   Task      `json:"task"`
	State  TaskState `json:"state"`
	Result *Result   `json:"result,omitempty"`
}

// HostInfo 主机注册信息与在线状态。
type HostInfo struct {
	ID       string            `json:"id"`
	Labels   map[string]string `json:"labels,omitempty"`
	Version  string            `json:"version,omitempty"`
	Online   bool              `json:"online"`
	LastSeen time.Time         `json:"last_seen"`
	Pending  int               `json:"pending"`
}

// HandshakeRequest Agent 启动时注册。
type HandshakeRequest struct {
	HostID  string            `json:"host_id"`
	Labels  map[string]string `json:"labels,omitempty"`
	Version string            `json:"version,omitempty"`
	OS      string            `json:"os,omitempty"`
	Arch    string            `json:"arch,omitempty"`
}

// HandshakeResponse 注册应答，携带服务端策略参数。
type HandshakeResponse struct {
	PollTimeoutS int    `json:"poll_timeout_s"`
	MaxOutput    int    `json:"max_output"`
	HostID       string `json:"host_id"`
}

// CreateTaskRequest 控制面创建任务。
type CreateTaskRequest struct {
	HostID    string            `json:"host_id"`
	Args      []string          `json:"args"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
	Idem      string            `json:"idem,omitempty"`
}

// ErrorResponse 统一错误体。
type ErrorResponse struct {
	Error string `json:"error"`
}
