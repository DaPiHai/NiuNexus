package proxy

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"niunexus/internal/config"
)

func startEcho(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close() }
}

func startEngine(t *testing.T, cfg config.ProxyConfig, secret Secret) (*Engine, Status) {
	t.Helper()
	engine := New()
	if err := engine.Start(cfg, secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Stop)
	status := engine.Status()
	if !status.Running || status.Listen == "" {
		t.Fatalf("engine not running: %+v", status)
	}
	return engine, status
}

func testConfig() config.ProxyConfig {
	return config.ProxyConfig{Enabled: true, Listen: "127.0.0.1", ListenPort: 0}
}

func TestHTTPConnectEcho(t *testing.T) {
	echoAddr, stop := startEcho(t)
	defer stop()
	_, status := startEngine(t, testConfig(), Secret{})
	conn, err := net.DialTimeout("tcp", status.Listen, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil || !strings.Contains(line, "200") {
		t.Fatalf("response = %q err=%v", line, err)
	}
	for {
		header, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if header == "\r\n" {
			break
		}
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("echo = %q", buf)
	}
}

func TestSOCKS5Connect(t *testing.T) {
	echoAddr, stop := startEcho(t)
	defer stop()
	_, status := startEngine(t, testConfig(), Secret{})
	conn, err := net.DialTimeout("tcp", status.Listen, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0x00 {
		t.Fatalf("method reply = %v err=%v", reply, err)
	}
	host, portText, _ := net.SplitHostPort(echoAddr)
	port, _ := strconv.Atoi(portText)
	ip := net.ParseIP(host).To4()
	request := []byte{0x05, 0x01, 0x00, 0x01}
	request = append(request, ip...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	request = append(request, portBytes...)
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil {
		t.Fatal(err)
	}
	if connectReply[1] != 0x00 {
		t.Fatalf("connect reply = %v", connectReply)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("echo = %q", buf)
	}
}

func TestHTTPAuth(t *testing.T) {
	echoAddr, stop := startEcho(t)
	defer stop()
	cfg := testConfig()
	cfg.RequireAuth = true
	cfg.Username = "user"
	_, status := startEngine(t, cfg, Secret{ProxyPassword: "pass"})

	conn, err := net.DialTimeout("tcp", status.Listen, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)
	reader := bufio.NewReader(conn)
	line, _ := reader.ReadString('\n')
	if !strings.Contains(line, "407") {
		t.Fatalf("expected 407, got %q", line)
	}

	conn2, err := net.DialTimeout("tcp", status.Listen, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	auth := "Basic dXNlcjpwYXNz" // user:pass
	fmt.Fprintf(conn2, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", echoAddr, echoAddr, auth)
	reader2 := bufio.NewReader(conn2)
	line2, _ := reader2.ReadString('\n')
	if !strings.Contains(line2, "200") {
		t.Fatalf("expected 200, got %q", line2)
	}
	for {
		header, _ := reader2.ReadString('\n')
		if header == "\r\n" {
			break
		}
	}
	_ = conn2.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn2.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(reader2, buf); err != nil || string(buf) != "ok" {
		t.Fatalf("echo = %q err=%v", buf, err)
	}
}

func TestSOCKS5Auth(t *testing.T) {
	echoAddr, stop := startEcho(t)
	defer stop()
	cfg := testConfig()
	cfg.RequireAuth = true
	cfg.Username = "user"
	_, status := startEngine(t, cfg, Secret{ProxyPassword: "pass"})
	conn, err := net.DialTimeout("tcp", status.Listen, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{0x05, 0x02, 0x00, 0x02}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0x02 {
		t.Fatalf("method = %v err=%v", reply, err)
	}
	auth := []byte{0x01, 0x04, 'u', 's', 'e', 'r', 0x04, 'p', 'a', 's', 's'}
	if _, err := conn.Write(auth); err != nil {
		t.Fatal(err)
	}
	authReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, authReply); err != nil || authReply[1] != 0x00 {
		t.Fatalf("auth = %v err=%v", authReply, err)
	}
	host, portText, _ := net.SplitHostPort(echoAddr)
	port, _ := strconv.Atoi(portText)
	ip := net.ParseIP(host).To4()
	request := []byte{0x05, 0x01, 0x00, 0x01}
	request = append(request, ip...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	request = append(request, portBytes...)
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	connectReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, connectReply); err != nil || connectReply[1] != 0x00 {
		t.Fatalf("connect = %v err=%v", connectReply, err)
	}
	if _, err := conn.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("echo = %q err=%v", buf, err)
	}
}

func TestBypassUsesDirectDial(t *testing.T) {
	echoAddr, stop := startEcho(t)
	defer stop()
	cfg := testConfig()
	cfg.Upstream = config.Upstream{Type: "http", Address: "127.0.0.1:1"}
	cfg.Bypass = []string{"127.0.0.0/8"}
	_, status := startEngine(t, cfg, Secret{})
	conn, err := net.DialTimeout("tcp", status.Listen, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)
	reader := bufio.NewReader(conn)
	line, _ := reader.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("expected 200, got %q", line)
	}
}

func TestStartDisabled(t *testing.T) {
	engine := New()
	if err := engine.Start(config.ProxyConfig{Enabled: false}, Secret{}); err != nil {
		t.Fatal(err)
	}
	if engine.Status().Running {
		t.Fatal("disabled engine should not be running")
	}
}
