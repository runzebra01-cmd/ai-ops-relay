// Command relay 是公网侧的中转服务。
//
// 用法:
//
//	relay serve   -addr :443 -agents ops1=<token> -api-token <token> -tls-cert cert.pem -tls-key key.pem
//	relay gencert -host relay.example.com -out /etc/relay
//	relay mcp     -api https://relay.example.com -token <token>
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"relay/internal/mcp"
	"relay/internal/policy"
	"relay/internal/relay"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[relay] ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "serve":
		serve(args)
	case "gencert":
		gencert(args)
	case "mcp":
		mcpCmd(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `relay <command> [flags]

commands:
  serve     启动中转服务
  gencert   生成自签 TLS 证书
  mcp       以 MCP stdio server 方式运行（给大模型用）
`)
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8443", "监听地址")
	apiToken := fs.String("api-token", os.Getenv("RELAY_API_TOKEN"), "控制面 API token")
	agents := fs.String("agents", "", "agent 凭据，格式 host1=token1,host2=token2")
	policyPath := fs.String("policy", "policy.json", "命令白名单文件")
	auditPath := fs.String("audit", "audit.jsonl", "审计日志路径（留空则不写）")
	certFile := fs.String("tls-cert", "", "TLS 证书；为空则用明文 HTTP（仅建议在反代后面）")
	keyFile := fs.String("tls-key", "", "TLS 私钥")
	sshProxy := fs.String("ssh-proxy", "", "同端口复用：把非 TLS 连接转给本地 sshd，例如 127.0.0.1:22")
	dlDir := fs.String("download-dir", "", "静态下载目录（配合 -download-token 使用）")
	dlToken := fs.String("download-token", os.Getenv("RELAY_DOWNLOAD_TOKEN"), "下载令牌")
	pollS := fs.Int("poll", 25, "长轮询挂起秒数")
	maxOut := fs.Int("max-out", 256<<10, "单个输出流最大字节数")
	_ = fs.Parse(args)

	if *apiToken == "" {
		log.Fatal("必须提供 -api-token（或用环境变量 RELAY_API_TOKEN）")
	}
	tokens := parseAgents(*agents)
	if len(tokens) == 0 {
		log.Fatal("至少需要一个 -agents host=token")
	}
	pol, err := policy.Load(*policyPath)
	if err != nil {
		log.Fatalf("加载策略失败: %v", err)
	}
	srv, err := relay.New(relay.Config{
		Addr:          *addr,
		APIToken:      *apiToken,
		AgentTokens:   tokens,
		Policy:        pol,
		AuditPath:     *auditPath,
		MaxOutput:     *maxOut,
		PollTimeout:   time.Duration(*pollS) * time.Second,
		DownloadDir:   *dlDir,
		DownloadToken: *dlToken,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Print("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	ln, lerr := net.Listen("tcp", *addr)
	if lerr != nil {
		log.Fatalf("listen %s: %v", *addr, lerr)
	}
	if *certFile != "" && *keyFile != "" {
		cert, cerr := tls.LoadX509KeyPair(*certFile, *keyFile)
		if cerr != nil {
			log.Fatalf("load tls cert: %v", cerr)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"http/1.1"},
		}
		var l net.Listener = tls.NewListener(ln, tlsCfg)
		if *sshProxy != "" {
			l = relay.SniffListener(ln, tlsCfg, *sshProxy, 500*time.Millisecond)
			log.Printf("端口复用已启用：SSH → %s，其余按 HTTPS 处理", *sshProxy)
		}
		log.Printf("listening on %s (TLS), hosts=%d", *addr, len(tokens))
		err = httpSrv.Serve(l)
	} else {
		log.Printf("listening on %s (plain http), hosts=%d", *addr, len(tokens))
		err = httpSrv.Serve(ln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func parseAgents(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		host, tok, ok := strings.Cut(pair, "=")
		if !ok || host == "" || tok == "" {
			log.Fatalf("非法 -agents 项: %q（应为 host=token）", pair)
		}
		out[tok] = host
	}
	return out
}

func gencert(args []string) {
	fs := flag.NewFlagSet("gencert", flag.ExitOnError)
	host := fs.String("host", "", "证书绑定的 IP 或域名（可逗号分隔多个）")
	out := fs.String("out", ".", "输出目录")
	days := fs.Int("days", 825, "有效期天数")
	_ = fs.Parse(args)
	if *host == "" {
		log.Fatal("必须提供 -host，例如 -host relay.example.com")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: strings.Split(*host, ",")[0], Organization: []string{"relay"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, *days),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	for _, h := range strings.Split(*host, ",") {
		h = strings.TrimSpace(h)
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatal(err)
	}
	certPath := *out + "/cert.pem"
	keyPath := *out + "/key.pem"
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s and %s (SAN: %s, %d days)\n", certPath, keyPath, *host, *days)
}

func mcpCmd(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	api := fs.String("api", "", "Relay 地址，例如 https://relay.example.com")
	token := fs.String("token", os.Getenv("RELAY_API_TOKEN"), "控制面 API token")
	ca := fs.String("ca", "", "Relay 自签证书路径")
	insecure := fs.Bool("insecure", false, "跳过 TLS 校验（仅测试）")
	_ = fs.Parse(args)
	if *api == "" || *token == "" {
		log.Fatal("必须提供 -api 和 -token")
	}
	srv := mcp.New(mcp.Config{
		APIURL:   *api,
		Token:    *token,
		CAFile:   *ca,
		Insecure: *insecure,
		Version:  version,
	})
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

var version = "0.1.0"
