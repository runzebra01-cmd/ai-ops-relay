//go:build unix

package agent

import "os"

func buildEnv(extra map[string]string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + homeOr("/root"),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TERM=dumb",
		"TZ=" + envOr("TZ", "UTC"),
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func homeOr(def string) string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return def
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
