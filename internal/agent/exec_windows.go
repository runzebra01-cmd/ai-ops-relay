//go:build windows

package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"relay/internal/protocol"
)

const createNoWindow = 0x08000000

// execute 在 Windows 上执行一条命令。超时时用 taskkill 结束整棵进程树，
// 因为 Windows 没有进程组，Process.Kill 只会杀掉父进程。
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

	argv := t.Args
	if isPowerShell(argv[0]) {
		argv = withUTF8Prologue(argv)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = t.Cwd
	cmd.Env = buildEnv(t.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out := &capWriter{limit: a.cfg.MaxOut}
	errw := &capWriter{limit: a.cfg.MaxOut}
	cmd.Stdout = out
	cmd.Stderr = errw
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		_ = kill.Run()
		return cmd.Process.Kill()
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
	res.Stdout = toUTF8(out.String())
	res.Stderr = toUTF8(errw.String())
	res.Truncated = out.truncated || errw.truncated
	return res
}

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
)

// toUTF8 修正中文 Windows 的编码问题：PowerShell/cmd 默认按系统 ANSI 代码页（简体中文是 CP936）
// 输出，而 Relay 全链路按 UTF-8 处理。合法 UTF-8 原样返回，否则按系统代码页转一次。
func toUTF8(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if utf8.Valid(b) {
		return s
	}
	return string(ansiToUTF8(b))
}

// ansiToUTF8 用 MultiByteToWideChar(CP_ACP) 把系统代码页字节转成 UTF-8。
func ansiToUTF8(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	const cpACP = 0 // CP_ACP：跟随系统当前 ANSI 代码页
	n, _, _ := procMultiByteToWideChar.Call(cpACP, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0, 0)
	if n == 0 {
		return b
	}
	buf := make([]uint16, n)
	r, _, _ := procMultiByteToWideChar.Call(cpACP, 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(n))
	if r == 0 {
		return b
	}
	return []byte(string(utf16.Decode(buf[:r])))
}

// utf8Prologue 让 PowerShell 从源头就按 UTF-8 输出。
// 不能靠"猜字节编码"：`目录` 的 GBK 字节 C4 BF C2 BC 恰好是合法 UTF-8，会被误判。
const utf8Prologue = "[Console]::OutputEncoding=[Text.Encoding]::UTF8;$OutputEncoding=[Text.Encoding]::UTF8;"

func isPowerShell(exe string) bool {
	base := strings.ToLower(exe)
	base = strings.TrimSuffix(base, ".exe")
	if i := strings.LastIndexAny(base, `\/`); i >= 0 {
		base = base[i+1:]
	}
	return base == "powershell" || base == "pwsh"
}

// withUTF8Prologue 只在 -Command/-c 后面的脚本前加一段固定的编码设置，
// 不改变用户命令本身；白名单校验在这之前已经由 Relay 按原始 argv 完成了。
func withUTF8Prologue(args []string) []string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out)-1; i++ {
		if strings.EqualFold(out[i], "-Command") || strings.EqualFold(out[i], "-c") {
			out[i+1] = utf8Prologue + out[i+1]
			return out
		}
	}
	return out
}
