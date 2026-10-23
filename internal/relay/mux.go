package relay

import (
	"crypto/tls"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// SniffListener 让 TLS(HTTPS) 和 SSH 共用同一个监听端口。
//
// 判定依据：SSH 是服务端先发 banner，客户端连上后通常什么都不发；
// 而 TLS 客户端会立刻发 ClientHello（首字节 0x16）。
// 所以"连接后静默超过 quiet"即判定为 SSH，转交给本地 sshd。
func SniffListener(raw net.Listener, tlsCfg *tls.Config, sshAddr string, quiet time.Duration) net.Listener {
	if quiet <= 0 {
		quiet = 500 * time.Millisecond
	}
	l := &sniffListener{
		raw:    raw,
		tlsCfg: tlsCfg,
		ssh:    sshAddr,
		quiet:  quiet,
		ch:     make(chan net.Conn),
		done:   make(chan struct{}),
	}
	go l.acceptLoop()
	return l
}

type sniffListener struct {
	raw    net.Listener
	tlsCfg *tls.Config
	ssh    string
	quiet  time.Duration
	ch     chan net.Conn
	done   chan struct{}
	once   sync.Once
}

func (l *sniffListener) acceptLoop() {
	for {
		c, err := l.raw.Accept()
		if err != nil {
			select {
			case <-l.done:
			default:
				log.Printf("sniff: accept error: %v", err)
			}
			return
		}
		go l.route(c)
	}
}

func (l *sniffListener) route(c net.Conn) {
	buf := make([]byte, 1)
	_ = c.SetReadDeadline(time.Now().Add(l.quiet))
	n, _ := c.Read(buf)
	_ = c.SetReadDeadline(time.Time{})

	if n == 0 {
		// 静默超时或对端直接关闭：SSH 服务端先说话，客户端此时不会发数据
		if l.ssh == "" {
			_ = c.Close()
			return
		}
		l.bridge(c, nil)
		return
	}
	if buf[0] == 0x16 {
		// TLS ClientHello
		l.toHTTP(c, buf[:n])
		return
	}
	if l.ssh != "" {
		// 少数客户端会主动先发 SSH banner
		l.bridge(c, buf[:n])
		return
	}
	l.toHTTP(c, buf[:n])
}

func (l *sniffListener) toHTTP(c net.Conn, pre []byte) {
	tc := tls.Server(&replayConn{Conn: c, pre: pre}, l.tlsCfg)
	select {
	case l.ch <- tc:
	case <-l.done:
		_ = c.Close()
	}
}

func (l *sniffListener) bridge(client net.Conn, pre []byte) {
	defer client.Close()
	upstream, err := net.DialTimeout("tcp", l.ssh, 3*time.Second)
	if err != nil {
		log.Printf("sniff: dial sshd %s: %v", l.ssh, err)
		return
	}
	defer upstream.Close()
	if len(pre) > 0 {
		if _, err := upstream.Write(pre); err != nil {
			return
		}
	}
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			_ = client.Close()
			_ = upstream.Close()
		})
	}
	go func() {
		_, _ = io.Copy(upstream, client)
		closeAll()
	}()
	_, _ = io.Copy(client, upstream)
	closeAll()
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *sniffListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.raw.Close()
}

func (l *sniffListener) Addr() net.Addr { return l.raw.Addr() }

// replayConn 把已经读出来的粘回首部，再交给 TLS。
type replayConn struct {
	net.Conn
	pre []byte
}

func (c *replayConn) Read(p []byte) (int, error) {
	if len(c.pre) > 0 {
		n := copy(p, c.pre)
		c.pre = c.pre[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}
