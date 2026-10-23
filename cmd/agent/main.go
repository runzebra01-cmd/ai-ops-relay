// Command agent 运行在内网机器上，主动外连 Relay。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"relay/internal/agent"
)

var version = "0.3.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[agent] ")

	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	relayURL := fs.String("relay", "", "Relay 地址，例如 https://relay.example.com")
	hostID := fs.String("host", "", "本机在 Relay 中的主机名（唯一）")
	token := fs.String("token", os.Getenv("RELAY_AGENT_TOKEN"), "主机令牌")
	labels := fs.String("labels", "", "标签，格式 env=prod,site=bj")
	ca := fs.String("ca", "", "Relay 证书路径（自签时使用）")
	insecure := fs.Bool("insecure", false, "跳过 TLS 校验（仅测试）")
	check := fs.Bool("check", false, "只做一次握手验证后退出")
	maxOut := fs.Int("max-out", 256<<10, "单次输出上限字节数")
	showVersion := fs.Bool("version", false, "打印版本")
	_ = fs.Parse(os.Args[1:])

	if *showVersion {
		fmt.Println("relay-agent", version)
		return
	}
	if *relayURL == "" || *hostID == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "必须提供 -relay、-host、-token")
		os.Exit(2)
	}
	if !strings.HasPrefix(*relayURL, "https://") && !*insecure {
		log.Print("警告: 使用明文 HTTP 传输，令牌和输出都会裸奔，仅建议内网测试")
	}

	a, err := agent.New(agent.Config{
		RelayURL: *relayURL,
		HostID:   *hostID,
		Token:    *token,
		Labels:   parseLabels(*labels),
		CAFile:   *ca,
		Insecure: *insecure,
		Version:  version,
		MaxOut:   *maxOut,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *check {
		if err := a.Check(ctx); err != nil {
			log.Fatalf("连接失败: %v", err)
		}
		return
	}
	log.Printf("relay-agent %s starting: relay=%s host=%s", version, *relayURL, *hostID)
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
	log.Print("stopped")
}

func parseLabels(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		if k != "" {
			out[k] = v
		}
	}
	return out
}
