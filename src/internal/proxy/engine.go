// Package proxy implements a small TCP proxy: HTTP CONNECT, SOCKS5, and a
// transparent listener that recovers the original destination via
// SO_ORIGINAL_DST. It deliberately does not decrypt TLS or proxy UDP.
package proxy

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"niunexus/internal/config"
)

// Secret is the credential payload supplied by the secrets store.
type Secret struct {
	ProxyPassword    string
	UpstreamPassword string
}

// Status is the proxy runtime snapshot.
type Status struct {
	Running           bool      `json:"running"`
	Listen            string    `json:"listen,omitempty"`
	TransparentListen string    `json:"transparentListen,omitempty"`
	Active            int64     `json:"active"`
	Total             int64     `json:"total"`
	BytesIn           int64     `json:"bytesIn"`
	BytesOut          int64     `json:"bytesOut"`
	StartedAt         time.Time `json:"startedAt,omitempty"`
}

// Engine owns the proxy listeners.
type Engine struct {
	mu          sync.Mutex
	cfg         config.ProxyConfig
	secret      Secret
	forward     net.Listener
	transparent net.Listener
	conns       map[net.Conn]struct{}
	wg          sync.WaitGroup
	active      int64
	total       int64
	bytesIn     int64
	bytesOut    int64
	startedAt   time.Time
}

// New creates an idle engine.
func New() *Engine {
	return &Engine{conns: map[net.Conn]struct{}{}}
}

// Start replaces any current listeners with the supplied configuration.
func (e *Engine) Start(cfg config.ProxyConfig, secret Secret) error {
	e.Stop()
	e.mu.Lock()
	e.cfg = cfg
	e.secret = secret
	e.conns = map[net.Conn]struct{}{}
	e.mu.Unlock()
	if !cfg.Enabled {
		return nil
	}

	listenHost := cfg.Listen
	if listenHost == "" {
		listenHost = "0.0.0.0"
	}
	forward, err := net.Listen("tcp", net.JoinHostPort(listenHost, strconv.Itoa(cfg.ListenPort)))
	if err != nil {
		return fmt.Errorf("启动正向代理监听失败: %w", err)
	}
	var transparent net.Listener
	if cfg.Transparent.Enabled {
		transparent, err = net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.TransparentPort)))
		if err != nil {
			_ = forward.Close()
			return fmt.Errorf("启动透明代理监听失败: %w", err)
		}
	}

	e.mu.Lock()
	e.forward = forward
	e.transparent = transparent
	e.startedAt = time.Now()
	e.mu.Unlock()

	e.wg.Add(1)
	go e.acceptLoop(forward, e.handleForward)
	if transparent != nil {
		e.wg.Add(1)
		go e.acceptLoop(transparent, e.handleTransparent)
	}
	return nil
}

// Stop closes listeners and active connections.
func (e *Engine) Stop() {
	e.mu.Lock()
	forward := e.forward
	transparent := e.transparent
	e.forward = nil
	e.transparent = nil
	for conn := range e.conns {
		_ = conn.Close()
	}
	e.conns = map[net.Conn]struct{}{}
	e.mu.Unlock()

	if forward != nil {
		_ = forward.Close()
	}
	if transparent != nil {
		_ = transparent.Close()
	}
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// Status returns counters and listener addresses.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	status := Status{
		Running:   e.forward != nil,
		Active:    atomic.LoadInt64(&e.active),
		Total:     atomic.LoadInt64(&e.total),
		BytesIn:   atomic.LoadInt64(&e.bytesIn),
		BytesOut:  atomic.LoadInt64(&e.bytesOut),
		StartedAt: e.startedAt,
	}
	if e.forward != nil {
		status.Listen = e.forward.Addr().String()
	}
	if e.transparent != nil {
		status.TransparentListen = e.transparent.Addr().String()
	}
	return status
}

func (e *Engine) acceptLoop(listener net.Listener, handler func(net.Conn)) {
	defer e.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			handler(conn)
		}()
	}
}

func (e *Engine) handleForward(conn net.Conn) {
	e.track(conn)
	defer e.untrack(conn)
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(conn)
	first, err := reader.Peek(1)
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if first[0] == 0x05 {
		e.handleSOCKS5(conn, reader)
		return
	}
	e.handleHTTP(conn, reader)
}

func (e *Engine) handleHTTP(conn net.Conn, reader *bufio.Reader) {
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	if !e.checkHTTPAuth(req.Header.Get("Proxy-Authorization")) {
		_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"NiuNexus\"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}

	if req.Method == http.MethodConnect {
		target := req.Host
		if target == "" {
			target = req.URL.Host
		}
		if target == "" {
			e.writeHTTPError(conn, http.StatusBadRequest, "missing CONNECT target")
			return
		}
		upstream, err := e.dialTarget(context.Background(), ensurePort(target, "443"))
		if err != nil {
			e.writeHTTPError(conn, http.StatusBadGateway, "upstream dial failed")
			return
		}
		defer upstream.Close()
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		e.copyBoth(conn, upstream)
		return
	}

	target := req.URL.Host
	if target == "" {
		target = req.Host
	}
	if target == "" {
		e.writeHTTPError(conn, http.StatusBadRequest, "missing absolute URL")
		return
	}
	upstream, err := e.dialTarget(context.Background(), ensurePort(target, "80"))
	if err != nil {
		e.writeHTTPError(conn, http.StatusBadGateway, "upstream dial failed")
		return
	}
	defer upstream.Close()

	req.RequestURI = ""
	req.URL.Scheme = ""
	req.URL.Host = ""
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.Header.Set("Connection", "close")
	if err := req.Write(upstream); err != nil {
		e.writeHTTPError(conn, http.StatusBadGateway, "upstream write failed")
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(upstream), req)
	if err != nil {
		e.writeHTTPError(conn, http.StatusBadGateway, "upstream read failed")
		return
	}
	defer resp.Body.Close()
	resp.Header.Set("Connection", "close")
	_ = resp.Write(conn)
}

func (e *Engine) handleSOCKS5(conn net.Conn, reader *bufio.Reader) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 0x05 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}
	if e.requireAuth() {
		if !containsByte(methods, 0x02) {
			_, _ = conn.Write([]byte{0x05, 0xff})
			return
		}
		if _, err := conn.Write([]byte{0x05, 0x02}); err != nil {
			return
		}
		if err := e.socksAuth(reader, conn); err != nil {
			return
		}
	} else {
		if !containsByte(methods, 0x00) {
			_, _ = conn.Write([]byte{0x05, 0xff})
			return
		}
		if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
			return
		}
	}

	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil || request[0] != 0x05 {
		return
	}
	if request[1] != 0x01 {
		_, _ = conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	host, err := readSocksHost(reader, request[3])
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	target := net.JoinHostPort(host, strconv.Itoa(port))
	upstream, err := e.dialTarget(context.Background(), target)
	if err != nil {
		_, _ = conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	e.copyBoth(conn, upstream)
}

func (e *Engine) handleTransparent(conn net.Conn) {
	e.track(conn)
	defer e.untrack(conn)
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	target, err := originalDestination(tcp)
	if err != nil {
		return
	}
	upstream, err := e.dialTarget(context.Background(), target)
	if err != nil {
		return
	}
	defer upstream.Close()
	e.copyBoth(conn, upstream)
}

func (e *Engine) dialTarget(ctx context.Context, target string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	cfg := e.cfg
	secret := e.secret
	e.mu.Unlock()
	if e.bypass(host, cfg.Bypass) {
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", target)
	}
	switch cfg.Upstream.Type {
	case "http":
		return e.dialHTTPUpstream(ctx, target, cfg.Upstream, secret.UpstreamPassword)
	case "socks5":
		return e.dialSOCKSUpstream(ctx, target, cfg.Upstream, secret.UpstreamPassword)
	default:
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", target)
	}
}

func (e *Engine) dialHTTPUpstream(ctx context.Context, target string, upstream config.Upstream, password string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", upstream.Address)
	if err != nil {
		return nil, err
	}
	var request strings.Builder
	fmt.Fprintf(&request, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if upstream.Username != "" || password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(upstream.Username + ":" + password))
		fmt.Fprintf(&request, "Proxy-Authorization: Basic %s\r\n", token)
	}
	request.WriteString("\r\n")
	if _, err := io.WriteString(conn, request.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("上游 HTTP 代理返回 %d", resp.StatusCode)
	}
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

func (e *Engine) dialSOCKSUpstream(ctx context.Context, target string, upstream config.Upstream, password string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", upstream.Address)
	if err != nil {
		return nil, err
	}
	if err := socksClientHandshake(conn, target, upstream.Username, password); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (e *Engine) copyBoth(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(countingWriter{w: a, counter: &e.bytesOut}, b)
		closeWrite(a)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(countingWriter{w: b, counter: &e.bytesIn}, a)
		closeWrite(b)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func (e *Engine) track(conn net.Conn) {
	e.mu.Lock()
	e.conns[conn] = struct{}{}
	e.mu.Unlock()
	atomic.AddInt64(&e.active, 1)
	atomic.AddInt64(&e.total, 1)
}

func (e *Engine) untrack(conn net.Conn) {
	_ = conn.Close()
	e.mu.Lock()
	delete(e.conns, conn)
	e.mu.Unlock()
	atomic.AddInt64(&e.active, -1)
}

func (e *Engine) checkHTTPAuth(header string) bool {
	e.mu.Lock()
	cfg := e.cfg
	secret := e.secret
	e.mu.Unlock()
	if !cfg.RequireAuth {
		return true
	}
	if !strings.HasPrefix(header, "Basic ") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(header, "Basic ")))
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return false
	}
	return constantEqual(parts[0], cfg.Username) && constantEqual(parts[1], secret.ProxyPassword)
}

func (e *Engine) requireAuth() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.RequireAuth
}

func (e *Engine) socksAuth(reader *bufio.Reader, conn net.Conn) error {
	version, err := reader.ReadByte()
	if err != nil || version != 0x01 {
		return errors.New("SOCKS5 auth version")
	}
	ulen, err := reader.ReadByte()
	if err != nil {
		return err
	}
	user := make([]byte, int(ulen))
	if _, err := io.ReadFull(reader, user); err != nil {
		return err
	}
	plen, err := reader.ReadByte()
	if err != nil {
		return err
	}
	pass := make([]byte, int(plen))
	if _, err := io.ReadFull(reader, pass); err != nil {
		return err
	}
	e.mu.Lock()
	cfg := e.cfg
	secret := e.secret
	e.mu.Unlock()
	if !constantEqual(string(user), cfg.Username) || !constantEqual(string(pass), secret.ProxyPassword) {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return errors.New("SOCKS5 auth failed")
	}
	_, err = conn.Write([]byte{0x01, 0x00})
	return err
}

func (e *Engine) bypass(host string, entries []string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err == nil && ip != nil && network.Contains(ip) {
				return true
			}
			continue
		}
		if parsed := net.ParseIP(entry); parsed != nil {
			if ip != nil && parsed.Equal(ip) {
				return true
			}
			continue
		}
		if host == entry || strings.HasSuffix(host, "."+entry) {
			return true
		}
	}
	return false
}

func (e *Engine) writeHTTPError(conn net.Conn, code int, message string) {
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, http.StatusText(code))
}

func readSocksHost(reader *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case 0x03:
		length, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		buf := make([]byte, int(length))
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	case 0x04:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	default:
		return "", errors.New("unsupported address type")
	}
}

func socksClientHandshake(conn net.Conn, target, username, password string) error {
	methods := []byte{0x00}
	if username != "" || password != "" {
		methods = []byte{0x02}
	}
	greeting := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[0] != 0x05 || response[1] == 0xff {
		return errors.New("upstream SOCKS5 rejected methods")
	}
	if response[1] == 0x02 {
		if len(username) > 255 || len(password) > 255 {
			return errors.New("SOCKS5 credentials too long")
		}
		auth := []byte{0x01, byte(len(username))}
		auth = append(auth, []byte(username)...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, []byte(password)...)
		if _, err := conn.Write(auth); err != nil {
			return err
		}
		reply := make([]byte, 2)
		if _, err := io.ReadFull(conn, reply); err != nil {
			return err
		}
		if reply[1] != 0x00 {
			return errors.New("upstream SOCKS5 auth failed")
		}
	}

	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid target port")
	}
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("target host too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, []byte(host)...)
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	request = append(request, portBytes...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		return fmt.Errorf("upstream SOCKS5 connect failed: %d", reply[1])
	}
	switch reply[3] {
	case 0x01:
		discard := make([]byte, 4+2)
		_, err = io.ReadFull(conn, discard)
	case 0x03:
		length := make([]byte, 1)
		if _, err = io.ReadFull(conn, length); err == nil {
			discard := make([]byte, int(length[0])+2)
			_, err = io.ReadFull(conn, discard)
		}
	case 0x04:
		discard := make([]byte, 16+2)
		_, err = io.ReadFull(conn, discard)
	default:
		return errors.New("upstream SOCKS5 bad address type")
	}
	return err
}

type countingWriter struct {
	w       io.Writer
	counter *int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	atomic.AddInt64(c.counter, int64(n))
	return n, err
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.reader.Read(p)
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

func containsByte(values []byte, want byte) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func constantEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func ensurePort(target, defaultPort string) string {
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target
	}
	if strings.HasPrefix(target, "[") && strings.HasSuffix(target, "]") {
		return target + ":" + defaultPort
	}
	if strings.Count(target, ":") > 1 {
		return "[" + target + "]:" + defaultPort
	}
	return net.JoinHostPort(target, defaultPort)
}

func originalDestination(conn *net.TCPConn) (string, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return "", err
	}
	var destination string
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		var addr unix.RawSockaddrInet4
		size := uint32(unsafe.Sizeof(addr))
		_, _, errno := unix.Syscall6(
			unix.SYS_GETSOCKOPT,
			fd,
			unix.SOL_IP,
			unix.SO_ORIGINAL_DST,
			uintptr(unsafe.Pointer(&addr)),
			uintptr(unsafe.Pointer(&size)),
			0,
		)
		if errno != 0 {
			sockErr = errno
			return
		}
		ip := net.IPv4(addr.Addr[0], addr.Addr[1], addr.Addr[2], addr.Addr[3])
		port := int(uint16(addr.Port)>>8 | uint16(addr.Port)<<8)
		destination = net.JoinHostPort(ip.String(), strconv.Itoa(port))
	}); err != nil {
		return "", err
	}
	if sockErr != nil {
		return "", sockErr
	}
	if destination == "" {
		return "", errors.New("无法获取原始目的地址")
	}
	return destination, nil
}
