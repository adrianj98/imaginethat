package faulttest

import (
	"bytes"
	"errors"
	"io"
	"net"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// echoServer echoes every connection back and counts the connections.
func echoServer(t *testing.T) (addr string, accepts *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepts = new(atomic.Int64)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String(), accepts
}

type received struct {
	n   int64
	err error // nil for a clean end
}

// sinkServer reads one connection to its end and reports what arrived.
func sinkServer(t *testing.T) (addr string, got <-chan received) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan received, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		n, err := io.Copy(io.Discard, c)
		ch <- received{n, err}
	}()
	return ln.Addr().String(), ch
}

func dial(t *testing.T, p *Proxy) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", p.Addr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return c
}

// roundTrip writes data and reads the same amount back.
func roundTrip(t *testing.T, c net.Conn, data []byte) []byte {
	t.Helper()
	go func() { _, _ = c.Write(data) }()
	got := make([]byte, len(data))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("reading %d bytes back: %v", len(data), err)
	}
	return got
}

// echoWorks reports whether a new connection through the proxy echoes.
func echoWorks(p *Proxy) bool {
	c, err := net.DialTimeout("tcp", p.Addr(), time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := c.Write([]byte("ping")); err != nil {
		return false
	}
	buf := make([]byte, 4)
	_, err = io.ReadFull(c, buf)
	return err == nil && string(buf) == "ping"
}

func waitFired(t *testing.T, p *Proxy) {
	t.Helper()
	select {
	case <-p.FiredCh():
	case <-time.After(3 * time.Second):
		t.Fatal("fault did not fire")
	}
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestForwardsUntouched(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	data := pattern(1 << 20)
	if got := roundTrip(t, dial(t, p), data); !bytes.Equal(got, data) {
		t.Fatal("bytes changed on the way through")
	}
	if p.Fired() {
		t.Error("Fired with nothing armed")
	}
	if up, down := p.Bytes(Up), p.Bytes(Down); up != 1<<20 || down != 1<<20 {
		t.Errorf("Bytes: up %d, down %d; want %d each", up, down, 1<<20)
	}
	if p.URL() != "http://"+p.Addr() {
		t.Errorf("URL = %q", p.URL())
	}
}

func TestCloseAtByte(t *testing.T) {
	addr, got := sinkServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 1000, Action: Close})
	c := dial(t, p)
	if _, err := c.Write(pattern(4000)); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.n != 1000 || r.err != nil {
		t.Errorf("target got %d bytes, err %v; want 1000 and a clean end", r.n, r.err)
	}
	if n, err := io.Copy(io.Discard, c); n != 0 || err != nil {
		t.Errorf("client read %d bytes, err %v; want a clean end", n, err)
	}
	if !p.Fired() {
		t.Error("Fired is false")
	}
}

func TestResetAtByte(t *testing.T) {
	addr, got := sinkServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 1000, Action: Reset})
	c := dial(t, p)
	_, _ = c.Write(pattern(4000))
	r := <-got
	// A reset may throw away bytes the target had not read yet.
	if r.n > 1000 || !errors.Is(r.err, syscall.ECONNRESET) {
		t.Errorf("target got %d bytes, err %v; want at most 1000 and a reset", r.n, r.err)
	}
	if _, err := io.Copy(io.Discard, c); !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("client read ended with %v, want a reset", err)
	}
}

func TestTruncateResponse(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	p.Arm(Truncate(500))
	c := dial(t, p)
	data := pattern(4000)
	go func() { _, _ = c.Write(data) }()
	got, err := io.ReadAll(c)
	if err != nil || !bytes.Equal(got, data[:500]) {
		t.Errorf("client got %d bytes, err %v; want the first 500 and a clean end", len(got), err)
	}
}

func TestStallThenContinues(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 100, Action: Stall, For: 150 * time.Millisecond})
	data := pattern(1000)
	start := time.Now()
	if got := roundTrip(t, dial(t, p), data); !bytes.Equal(got, data) {
		t.Fatal("bytes changed across a stall")
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("took %v, want at least the 150ms stall", d)
	}
}

func TestStallEndsOnHeal(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 100, Action: Stall, For: time.Hour})
	c := dial(t, p)
	data := pattern(1000)
	go func() { _, _ = c.Write(data) }()
	waitFired(t, p)
	p.Heal()
	got := make([]byte, len(data))
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("after Heal: %v", err)
	}
}

func TestBlackholeUntilHeal(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 10, Action: Blackhole})
	c := dial(t, p)
	if _, err := c.Write(pattern(100)); err != nil {
		t.Fatal(err)
	}
	waitFired(t, p)
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	n, err := io.ReadFull(c, make([]byte, 100))
	var ne net.Error
	if n > 10 || !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("read %d bytes, err %v; want at most 10 and a timeout", n, err)
	}
	if echoWorks(p) {
		t.Error("a new connection got through the blackhole")
	}
	p.Heal()
	if !echoWorks(p) {
		t.Error("no echo on a new connection after Heal")
	}
	// The connection that lost bytes stays silent: its peer is gone and no
	// reset ever comes, which is what a half-open connection looks like.
	_, _ = c.Write(pattern(100))
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := io.ReadFull(c, make([]byte, 100)); n != 0 || !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("after Heal the old connection read %d bytes, err %v; want silence", n, err)
	}
}

func TestOutageRefusesThenHeals(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 10, Action: Outage})
	c := dial(t, p)
	_, _ = c.Write(pattern(100))
	waitFired(t, p)
	if _, err := io.Copy(io.Discard, c); err == nil {
		t.Error("the open connection ended cleanly, want a reset")
	}
	if echoWorks(p) {
		t.Error("a new connection got through the outage")
	}
	p.Heal()
	if !echoWorks(p) {
		t.Error("no echo after Heal")
	}
}

func TestOutageHealsByItself(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 10, Action: Outage, For: 100 * time.Millisecond})
	_, _ = dial(t, p).Write(pattern(100))
	waitFired(t, p)
	if echoWorks(p) {
		t.Error("a new connection got through the outage")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !echoWorks(p) {
		if time.Now().After(deadline) {
			t.Fatal("still down 2s after a 100ms outage")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestArmStartsCountingFromZero(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	c := dial(t, p)
	roundTrip(t, c, pattern(5000))
	p.Arm(Fault{Dir: Up, At: 1000, Action: Close})
	roundTrip(t, c, pattern(500))
	if p.Fired() {
		t.Fatal("fired 500 bytes after Arm; the 5000 before it were counted")
	}
	_, _ = c.Write(pattern(1000))
	waitFired(t, p)
}

func TestFiresOnce(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 10, Action: Close})
	_, _ = dial(t, p).Write(pattern(100))
	waitFired(t, p)
	data := pattern(1 << 20)
	if got := roundTrip(t, dial(t, p), data); !bytes.Equal(got, data) {
		t.Fatal("the fault fired a second time")
	}
}

func TestFiredChIsNewPerArm(t *testing.T) {
	addr, _ := echoServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 10, Action: Stall, For: time.Millisecond})
	c := dial(t, p)
	roundTrip(t, c, pattern(100))
	waitFired(t, p)
	p.Arm(Fault{Dir: Up, At: 10, Action: Stall, For: time.Millisecond})
	select {
	case <-p.FiredCh():
		t.Fatal("the new fault reads as fired before any bytes")
	default:
	}
	if p.Fired() {
		t.Fatal("Fired is true right after Arm")
	}
	roundTrip(t, c, pattern(100))
	waitFired(t, p)
}

func TestSetTarget(t *testing.T) {
	first, firstAccepts := echoServer(t)
	second, secondAccepts := echoServer(t)
	p := NewProxy(t, first)
	if !echoWorks(p) {
		t.Fatal("no echo from the first target")
	}
	p.SetTarget(second)
	if !echoWorks(p) {
		t.Fatal("no echo from the second target")
	}
	if firstAccepts.Load() != 1 || secondAccepts.Load() != 1 {
		t.Errorf("accepts: first %d, second %d; want 1 and 1", firstAccepts.Load(), secondAccepts.Load())
	}
}

func TestCloseStopsEverything(t *testing.T) {
	addr, _ := echoServer(t)
	before := runtime.NumGoroutine()
	p := NewProxy(t, addr)
	for range 20 {
		roundTrip(t, dial(t, p), pattern(100))
	}
	p.Close()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines, %d before the proxy", runtime.NumGoroutine(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if echoWorks(p) {
		t.Error("a closed proxy still forwards")
	}
}

// A connection left dead by a blackhole stays silent while both ends hold
// on. Once one end gives up, the other is cut too, which stands for the
// keepalive that tells a real server its client is gone.
func TestDeadConnectionEndsWhenOneSideLeaves(t *testing.T) {
	addr, got := sinkServer(t)
	p := NewProxy(t, addr)
	p.Arm(Fault{Dir: Up, At: 10, Action: Blackhole})
	c := dial(t, p)
	if _, err := c.Write(pattern(100)); err != nil {
		t.Fatal(err)
	}
	waitFired(t, p)
	p.Heal()
	select {
	case r := <-got:
		t.Fatalf("the target's connection ended while the client still held on: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}
	c.Close()
	select {
	case r := <-got:
		if r.n > 10 {
			t.Errorf("target got %d bytes, want at most the 10 before the blackhole", r.n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the target's connection is still open 2s after the client left")
	}
}
