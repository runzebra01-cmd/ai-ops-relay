//go:build unix

package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"relay/internal/protocol"
)

// execute 在隔离的进程组里执行一条命令，超时会杀掉整个进程组。
func (a *Agent) execute(t *protocol.Task) *protocol.Result {
	res := &protocol.Result{
		TaskID:    t.ID,
		HostID:    a.cfg.HostID,
		StartedAt: time.Now().UTC(),
		ExitCode:  -1,
	}
	defer func() {
		res.FinishedAt = time.Now().UTC()
		res.DurationMS = res.FinishedAt.Sub(res.StartedAt).Milliseconds()
	}()

	if len(t.Args) == 0 {
		res.Err = "empty argv"
		return res
	}
	timeout := time.Duration(t.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.Args[0], t.Args[1:]...)
	cmd.Dir = t.Cwd
	cmd.Env = buildEnv(t.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &capWriter{limit: a.cfg.MaxOut}
	errw := &capWriter{limit: a.cfg.MaxOut}
	cmd.Stdout = out
	cmd.Stderr = errw
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	switch {
	case err == nil:
		res.ExitCode = 0
	case errors.Is(err, exec.ErrNotFound):
		res.ExitCode = 127
		res.Err = "command not found: " + t.Args[0]
	default:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
			res.Err = err.Error()
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		res.Err = fmt.Sprintf("timeout after %s", timeout)
		if res.ExitCode == 0 {
			res.ExitCode = -1
		}
	}
	res.Stdout = out.String()
	res.Stderr = errw.String()
	res.Truncated = out.truncated || errw.truncated
	return res
}
