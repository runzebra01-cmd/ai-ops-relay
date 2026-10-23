package policy

import "testing"

func TestDefaultPolicy(t *testing.T) {
	c := Default()
	cases := []struct {
		args []string
		ok   bool
	}{
		{[]string{"tail", "-n", "100", "/var/log/app.log"}, true},
		{[]string{"journalctl", "-u", "nginx", "-n", "50"}, true},
		{[]string{"systemctl", "status", "nginx"}, true},
		{[]string{"rm", "-rf", "/tmp/x"}, false},
		{[]string{"bash", "-c", "echo hi"}, false},
		{[]string{"cat", "/etc/shadow"}, true}, // 路径白名单暂不限制，靠权限兜底
		{[]string{}, false},
	}
	for _, tc := range cases {
		if err := c.Check("h1", tc.args); (err == nil) != tc.ok {
			t.Errorf("Check(%v) = %v, want ok=%v", tc.args, err, tc.ok)
		}
	}
}

func TestWildcardRule(t *testing.T) {
	c := Default()
	c.Hosts = map[string]Override{
		"win-01": {
			Mode: ModeExec,
			Allow: [][]string{
				{"powershell.exe", "-NoProfile", "-Command", "Get-Content*"},
			},
		},
	}
	ok := []string{"powershell.exe", "-NoProfile", "-Command", "Get-Content -Tail 100 C:\\app\\x.log"}
	if err := c.Check("win-01", ok); err != nil {
		t.Errorf("应当允许: %v", err)
	}
	bad := []string{"powershell.exe", "-NoProfile", "-Command", "Get-Content x; Remove-Item -Recurse C:\\"}
	if err := c.Check("win-01", bad); err == nil {
		t.Error("带分号的命令必须被拒绝")
	}
	bad2 := []string{"powershell.exe", "-NoProfile", "-Command", "Invoke-WebRequest http://x"}
	if err := c.Check("win-01", bad2); err == nil {
		t.Error("未在白名单的命令必须被拒绝")
	}
	if err := c.Check("other-host", ok); err == nil {
		t.Error("策略不能泄漏到其它主机")
	}
}

func TestSSHJumpPolicy(t *testing.T) {
	c := Default()
	c.Hosts = map[string]Override{
		"jump": {
			Mode: ModeExec,
			SSH: &SSHPolicy{
				Key:   "/tmp/relay_jump",
				CIDRs: []string{"10.20.30.0/24"},
				Ports: []int{22, 1022},
				Users: []string{"ops"},
			},
		},
	}
	base := []string{"ssh", "-i", "/tmp/relay_jump", "-o", "BatchMode=yes", "-p", "1022"}

	cases := []struct {
		name string
		args []string
		ok   bool
	}{
		{"网段内任意主机", append(append([]string{}, base...), "ops@10.20.30.101", "tail -n 100 /var/log/syslog"), true},
		{"网段内另一台", append(append([]string{}, base...), "ops@10.20.30.55", "df -h"), true},
		{"网段外拒绝", append(append([]string{}, base...), "ops@10.20.31.10", "df -h"), false},
		{"公网地址拒绝", append(append([]string{}, base...), "ops@192.0.2.10", "id"), false},
		{"主机名拒绝", append(append([]string{}, base...), "ops@db.internal", "id"), false},
		{"端口转发-L拒绝", append(append([]string{}, base...), "-L", "8080:127.0.0.1:80", "ops@10.20.30.101"), false},
		{"动态转发-D拒绝", append(append([]string{}, base...), "-D", "1080", "ops@10.20.30.101"), false},
		{"ProxyCommand拒绝", []string{"ssh", "-i", "/tmp/relay_jump", "-o", "ProxyCommand=curl evil", "ops@10.20.30.101"}, false},
		{"错误密钥拒绝", []string{"ssh", "-i", "/tmp/other_key", "-o", "BatchMode=yes", "-p", "1022", "ops@10.20.30.101", "id"}, false},
		{"非授权端口拒绝", []string{"ssh", "-i", "/tmp/relay_jump", "-o", "BatchMode=yes", "-p", "2222", "ops@10.20.30.101", "id"}, false},
		{"非授权用户拒绝", append(append([]string{}, base...), "root@10.20.30.101", "id"), false},
	}
	for _, tc := range cases {
		err := c.Check("jump", tc.args)
		if (err == nil) != tc.ok {
			t.Errorf("%s: Check(%v) = %v, want ok=%v", tc.name, tc.args, err, tc.ok)
		}
	}
}

func TestSSHUnrestricted(t *testing.T) {
	c := Default()
	c.Hosts = map[string]Override{
		"open": {Mode: ModeExec, SSH: &SSHPolicy{Unrestricted: true}},
	}
	ok := [][]string{
		{"ssh", "root@192.0.2.10", "id"},
		{"ssh", "-i", "/any/key", "-L", "8080:127.0.0.1:80", "admin@10.0.0.9"},
		{"ssh", "-o", "ProxyCommand=curl x", "x@192.0.2.20"},
	}
	for _, a := range ok {
		if err := c.Check("open", a); err != nil {
			t.Errorf("Unrestricted 应放行 %v, got %v", a, err)
		}
	}
	if err := c.Check("open", []string{"curl", "http://x"}); err == nil {
		t.Error("非 ssh 命令仍应被 allow 列表约束")
	}
}
