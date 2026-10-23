// Package policy 实现 Relay 侧的命令白名单。默认拒绝，彻底不走 shell。
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Mode 决定一台机器的放行策略。
type Mode string

const (
	// ModeReadOnly 只允许 Allow 列表里的只读命令（tail/journalctl/ps 等）。
	ModeReadOnly Mode = "readonly"
	// ModeExec 允许 Allow 列表里的命令（可以由运维扩充到重启服务等）。
	ModeExec Mode = "exec"
	// ModeFull 除 Deny 外全部放行。仅供调试，默认不要用。
	ModeFull Mode = "full"
)

// Override 针对单台主机的策略覆盖。
type Override struct {
	Mode  Mode       `json:"mode,omitempty"`
	Allow [][]string `json:"allow,omitempty"`
	Deny  [][]string `json:"deny,omitempty"`
	SSH   *SSHPolicy `json:"ssh,omitempty"`
}

// Config policy.json 的结构。
type Config struct {
	Mode  Mode                `json:"mode"`
	Allow [][]string          `json:"allow"`
	Deny  [][]string          `json:"deny"`
	Hosts map[string]Override `json:"hosts"`
	// SSH 全局跳板策略，可被 hosts 里的同名配置覆盖
	SSH *SSHPolicy `json:"ssh,omitempty"`
}

// SSHPolicy 定义"这台机器能通过 ssh 跳到哪些目标"。
// 不按单机写死，而是按网段授权；同时禁止一切端口转发选项，
// 避免跳板被当成通用 TCP 代理使用。
type SSHPolicy struct {
	// Key 必须匹配的私钥路径（-i 参数）
	Key string `json:"key"`
	// CIDRs 允许访问的目标网段，目标必须是 IP 字面量，不接受主机名（防 DNS 重绑）
	CIDRs []string `json:"cidrs"`
	// Ports 允许的目标端口
	Ports []int `json:"ports"`
	// Users 允许的登录用户，留空表示不限制
	Users []string `json:"users"`
	// Unrestricted 为 true 时，跳过目标/端口/用户/选项的全部校验，
	// 只要可执行文件是 ssh 就放行。适合"要连整个局域网所有设备"的场景。
	Unrestricted bool `json:"unrestricted,omitempty"`

	cidrs []*net.IPNet
}

// ErrDenied 表示命令被策略拒绝。
type ErrDenied struct {
	Reason string
}

func (e *ErrDenied) Error() string { return "denied: " + e.Reason }

var errNoRule = &ErrDenied{Reason: "no matching allow rule"}

// Default 返回内置的只读策略，用于第一次启动。
func Default() *Config {
	return &Config{
		Mode: ModeReadOnly,
		Allow: [][]string{
			{"tail"},
			{"head"},
			{"cat"},
			{"less"},
			{"grep"},
			{"journalctl"},
			{"systemctl", "status"},
			{"systemctl", "list-units"},
			{"ps"},
			{"top", "-b", "-n", "1"},
			{"df"},
			{"du"},
			{"free"},
			{"uptime"},
			{"uname"},
			{"ls"},
			{"stat"},
			{"who"},
			{"last"},
			{"ss"},
			{"netstat"},
			{"ip", "addr"},
			{"dmesg"},
			{"docker", "ps"},
			{"docker", "logs"},
		},
		Deny: [][]string{
			{"rm"},
			{"shutdown"},
			{"reboot"},
			{"mkfs"},
			{"dd"},
			{"chmod"},
			{"chown"},
			{"sudo"},
			{"su"},
			{"curl"},
			{"wget"},
			{"nc"},
			{"bash"},
			{"sh"},
			{"python"},
			{"perl"},
		},
	}
}

// Load 读取策略文件；文件不存在时返回内置默认策略。
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Mode == "" {
		c.Mode = ModeReadOnly
	}
	return c, nil
}

// Check 校验 argv 是否允许在 hostID 上执行。
func (c *Config) Check(hostID string, args []string) error {
	if len(args) == 0 {
		return &ErrDenied{Reason: "empty argv"}
	}
	mode, allow, deny := c.Mode, c.Allow, c.Deny
	if o, ok := c.Hosts[hostID]; ok {
		if o.Mode != "" {
			mode = o.Mode
		}
		if len(o.Allow) > 0 {
			allow = append(append([][]string{}, allow...), o.Allow...)
		}
		if len(o.Deny) > 0 {
			deny = append(append([][]string{}, deny...), o.Deny...)
		}
	}

	for _, d := range deny {
		if hasPrefix(args, d) {
			return &ErrDenied{Reason: "matches deny rule " + strings.Join(d, " ")}
		}
	}
	if ok := c.checkSSH(hostID, args); ok {
		return nil
	}
	if mode == ModeFull {
		return nil
	}
	for _, a := range allow {
		if matchRule(args, a) {
			return nil
		}
	}
	return errNoRule
}

// matchRule 支持末位通配：["powershell.exe","-Command","Get-Content*"]。
// 通配段的实参里不允许出现 shell 元字符，否则 PowerShell 可以用 "; Remove-Item" 绕过白名单。
func matchRule(args, rule []string) bool {
	if len(rule) == 0 {
		return false
	}
	last := rule[len(rule)-1]
	if !strings.HasSuffix(last, "*") {
		return hasPrefix(args, rule)
	}
	head := rule[:len(rule)-1]
	if len(args) < len(rule) {
		return false
	}
	if !hasPrefix(args, head) {
		return false
	}
	pat := strings.TrimSuffix(last, "*")
	actual := args[len(head)]
	if !strings.HasPrefix(actual, pat) {
		return false
	}
	return !hasShellMeta(actual)
}

// hasShellMeta 拒绝会把一条命令变成多条的危险字符。
func hasShellMeta(s string) bool {
	return strings.ContainsAny(s, ";|&`$()<>\n\r")
}

func hasPrefix(args, prefix []string) bool {
	if len(prefix) > len(args) {
		return false
	}
	for i := range prefix {
		if args[i] != prefix[i] {
			return false
		}
	}
	return true
}

// Describe 给 MCP / 控制面展示当前策略摘要。
func (c *Config) Describe(hostID string) string {
	mode := c.Mode
	if o, ok := c.Hosts[hostID]; ok && o.Mode != "" {
		mode = o.Mode
	}
	return string(mode)
}

// sshFor 返回该主机生效的 SSH 策略（主机级优先于全局）。
func (c *Config) sshFor(hostID string) *SSHPolicy {
	if o, ok := c.Hosts[hostID]; ok && o.SSH != nil {
		return o.SSH
	}
	return c.SSH
}

// allowedOption 只放行明确列出的 -o 选项，其余（尤其是 ProxyCommand）一律拒绝。
func allowedOption(opt string) bool {
	name, _, _ := strings.Cut(opt, "=")
	switch strings.ToLower(name) {
	case "batchmode", "stricthostkeychecking", "userknownhostsfile",
		"connecttimeout", "serveraliveinterval", "loglevel", "preferredauthentications":
		return true
	}
	return false
}

// checkSSH 解析 ssh 命令行并校验目标是否在授权网段内。
// 只接受 IP 字面量，避免跳板机自身的 DNS 解析结果与 Relay 不一致带来绕过。
func (c *Config) checkSSH(hostID string, args []string) bool {
	p := c.sshFor(hostID)
	if p == nil || len(args) < 1 {
		return false
	}
	base := strings.ToLower(filepath.Base(args[0]))
	base = strings.TrimSuffix(base, ".exe")
	if base != "ssh" {
		return false
	}
	if p.Unrestricted {
		return true
	}
	if len(args) < 2 {
		return false
	}
	if p.cidrs == nil {
		for _, s := range p.CIDRs {
			if _, n, err := net.ParseCIDR(s); err == nil {
				p.cidrs = append(p.cidrs, n)
			}
		}
	}

	var key string
	port := 22
	var user string
	i := 1
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		switch {
		case a == "-i" && i+1 < len(args):
			i++
			key = args[i]
		case strings.HasPrefix(a, "-i") && len(a) > 2:
			key = a[2:]
		case a == "-p" && i+1 < len(args):
			i++
			if _, err := fmt.Sscanf(args[i], "%d", &port); err != nil {
				return false
			}
		case strings.HasPrefix(a, "-p") && len(a) > 2:
			if _, err := fmt.Sscanf(a[2:], "%d", &port); err != nil {
				return false
			}
		case a == "-l" && i+1 < len(args):
			i++
			user = args[i]
		case a == "-o" && i+1 < len(args):
			i++
			if !allowedOption(args[i]) {
				return false
			}
		case strings.HasPrefix(a, "-o") && len(a) > 2:
			if !allowedOption(a[2:]) {
				return false
			}
		default:
			// -L -R -D -W -J -F 等一律拒绝，杜绝端口转发和配置劫持
			return false
		}
		i++
	}
	if i >= len(args) {
		return false
	}
	target := args[i]
	if u, h, ok := strings.Cut(target, "@"); ok {
		user = u
		target = h
	}
	if key == "" || strings.TrimSpace(p.Key) == "" || filepath.Clean(key) != filepath.Clean(p.Key) {
		return false
	}
	portOK := len(p.Ports) == 0
	for _, pp := range p.Ports {
		if pp == port {
			portOK = true
			break
		}
	}
	if !portOK {
		return false
	}
	if len(p.Users) > 0 {
		userOK := false
		for _, u := range p.Users {
			if u == user {
				userOK = true
				break
			}
		}
		if !userOK {
			return false
		}
	}
	ip := net.ParseIP(target)
	if ip == nil {
		return false // 只接受 IP 字面量
	}
	for _, n := range p.cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
