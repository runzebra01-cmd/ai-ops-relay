//go:build windows

package agent

import (
	"os"
	"strings"
)

// sensitive 命中这些关键字的变量不传给子进程，避免把凭据顺手递给被执行的命令。
var sensitive = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL",
	"APIKEY", "API_KEY", "PRIVATE", "ACCESS_KEY",
}

func isSensitive(name string) bool {
	up := strings.ToUpper(name)
	for _, s := range sensitive {
		if strings.Contains(up, s) {
			return true
		}
	}
	return false
}

// buildEnv 在 Windows 上继承父进程环境，但剔除疑似凭据的变量。
//
// 早期版本用的是白名单，结果裁掉了 APPDATA/USERNAME/HOMEDRIVE 等变量，
// 导致 OpenSSH 这类程序静默退出（exit 255，无任何输出），很难排查。
// 继承 + 黑名单在兼容性和安全性之间更平衡。
func buildEnv(extra map[string]string) []string {
	env := make([]string, 0, 64)
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" || isSensitive(name) {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
