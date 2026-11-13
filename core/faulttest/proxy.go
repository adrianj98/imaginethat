// Package faulttest injects the faults a copy has to survive: a TCP proxy
// that cuts, resets, stalls and blackholes connections at a chosen byte, and
// a filesystem wrapper that fails reads, writes and renames. Only tests
// import it.
package faulttest

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Dir is the direction a fault counts bytes in.
type Dir int

const (
	Up   Dir = iota // client to target
	Down            // target to client
)

// Action is what a fault does when it fires.
type Action int

const (
	// Close ends the connection cleanly, in both directions.
	Close Action = iota
	// Reset ends it with a TCP reset.
	Reset
	// Stall stops forwarding in the fault's direction for Fault.For, or
	// until Heal, then carries on. The connection stays open.
	Stall
	// Blackhole swallows every byte, on every connection and in both
	// directions, until Heal. Neither side sees an error. After Heal new
	// connections work, and the ones that were open stay silent: their peer
	// is gone and no reset comes, like a half-open connection after a
	// router or server lost its state. When one end of such a connection
	// gives up and closes, the other end is reset, standing for the TCP
	// keepalive that would tell it.
	Blackhole
	// Outage resets the open connections and refuses new ones for
	// Fault.For, or until Heal when For is zero.
	Outage
)

// Fault is one failure, armed with Proxy.Arm.
type Fault struct {
	Dir Dir
	// At is how many bytes pass in Dir, counted over all connections from
	// the moment the fault is armed, before it fires. Those bytes are
	// delivered; the ones after them meet the action.
	At     int64
	Action Action
	For    time.Duration
}

// Truncate is a response that ends cleanly after at bytes.
func Truncate(at int64) Fault { return Fault{Dir: Down, At: at, Action: Close} }

// Proxy forwards TCP connections to a target and applies one armed fault.
type Proxy struct {
	ln   net.Listener
	wg   sync.WaitGroup
	done chan struct{}
	once sync.Once

	mu        sync.Mutex
	target    string
	conns     map[*pair]struct{}
	fault     *Fault
	count     [2]int64
	total     [2]int64
	fired     bool
	firedCh   chan struct{}
	healCh    chan struct{}
	gen       int
	blackhole bool
	outage    bool
	closed    bool
}

// NewProxy starts a proxy on a free local port in front of target
// (host:port). It is closed when the test ends.
func NewProxy(t testing.TB, target string) *Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("faulttest: listen: %v", err)
	}
	p := &Proxy{
		ln:      ln,
		done:    make(chan struct{}),
		target:  target,
		conns:   make(map[*pair]struct{}),
		firedCh: make(chan struct{}),
		healCh:  make(chan struct{}),
	}
	p.wg.Add(1)
	go p.accept()
	t.Cleanup(p.Close)
	return p
}

// Addr is the proxy's host:port.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// URL is the proxy's address as an http base URL.
func (p *Proxy) URL() string { return "http://" + p.Addr() }

// SetTarget sends new connections to another target.
func (p *Proxy) SetTarget(target string) {
	p.mu.Lock()
	p.target = target
	p.mu.Unlock()
}

// Arm replaces the armed fault and starts its byte counts from zero. A fault
// fires once. A blackhole or outage already under way lasts until Heal.
func (p *Proxy) Arm(f Fault) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fault = &f
	p.count = [2]int64{}
	p.fired = false
	p.firedCh = make(chan struct{})
	p.gen++
}

// Bytes is how many bytes have been passed on in direction d since the proxy
// started, over all connections. Swallowed bytes do not count.
func (p *Proxy) Bytes(d Dir) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total[d]
}

// Heal ends a stall, a blackhole or an outage.
func (p *Proxy) Heal() { p.heal(-1) }

// heal ends the current trouble; with gen >= 0, only if no Arm or Heal has
// happened since that generation (an outage's own timer).
func (p *Proxy) heal(gen int) {
	p.mu.Lock()
	if gen >= 0 && gen != p.gen {
		p.mu.Unlock()
		return
	}
	var holed []*pair
	if p.blackhole {
		for pr := range p.conns {
			holed = append(holed, pr)
		}
	}
	p.blackhole, p.outage = false, false
	p.gen++
	close(p.healCh)
	p.healCh = make(chan struct{})
	p.mu.Unlock()
	for _, pr := range holed {
		pr.dead.Store(true)
	}
}

// Fired reports whether the armed fault has fired.
func (p *Proxy) Fired() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fired
}

// FiredCh is closed when the armed fault fires. Each Arm makes a new one.
func (p *Proxy) FiredCh() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.firedCh
}

// Close stops the proxy and returns once every connection has ended.
func (p *Proxy) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.ln.Close()
		close(p.done)
		p.resetAll()
	})
	p.wg.Wait()
}

func (p *Proxy) resetAll() {
	p.mu.Lock()
	all := make([]*pair, 0, len(p.conns))
	for pr := range p.conns {
		all = append(all, pr)
	}
	p.mu.Unlock()
	for _, pr := range all {
		pr.reset()
	}
}

func (p *Proxy) accept() {
	defer p.wg.Done()
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		refuse := p.closed || p.outage
		target := p.target
		p.mu.Unlock()
		if refuse {
			resetConn(client)
			continue
		}
		p.wg.Add(1)
		go p.serve(client, target)
	}
}

func (p *Proxy) serve(client net.Conn, target string) {
	defer p.wg.Done()
	upstream, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		resetConn(client)
		return
	}
	pr := &pair{client: client, target: upstream}
	p.mu.Lock()
	if p.closed || p.outage {
		p.mu.Unlock()
		pr.reset()
		return
	}
	p.conns[pr] = struct{}{}
	p.mu.Unlock()

	var both sync.WaitGroup
	both.Add(2)
	go func() { defer both.Done(); p.forward(pr, Up) }()
	go func() { defer both.Done(); p.forward(pr, Down) }()
	both.Wait()
	pr.shut()
	p.mu.Lock()
	delete(p.conns, pr)
	p.mu.Unlock()
}

func (p *Proxy) forward(pr *pair, d Dir) {
	src, dst := pr.client, pr.target
	if d == Down {
		src, dst = pr.target, pr.client
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		// After a clean close the rest is read and dropped, so that closing
		// with unread bytes does not turn into a reset.
		if n > 0 && !pr.closing.Load() && !pr.dead.Load() {
			if stop := p.deliver(pr, d, buf[:n], dst); stop {
				pr.shut()
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && !pr.closing.Load() {
				if pr.dead.Load() {
					// One end of a dead connection gave up. Cut the other
					// too: a real peer would learn of it from keepalive.
					pr.reset()
					return
				}
				// One side finished sending; the other may still be.
				closeWrite(dst)
				return
			}
			pr.shut()
			return
		}
	}
}

// deliver passes buf on, applying the fault if it fires inside it. It
// reports whether the connection is finished.
func (p *Proxy) deliver(pr *pair, d Dir, buf []byte, dst net.Conn) (stop bool) {
	for len(buf) > 0 {
		allow, swallow, act, healed := p.account(d, len(buf))
		if swallow {
			return false
		}
		if allow > 0 {
			n, err := dst.Write(buf[:allow])
			p.mu.Lock()
			p.total[d] += int64(n)
			p.mu.Unlock()
			if err != nil {
				return true
			}
		}
		buf = buf[allow:]
		if act == nil {
			continue
		}
		switch act.Action {
		case Close:
			pr.closeClean()
			return false
		case Reset:
			pr.reset()
			return true
		case Stall:
			t := time.NewTimer(act.For)
			select {
			case <-t.C:
			case <-healed:
			case <-p.done:
			}
			t.Stop()
		case Blackhole:
			return false
		case Outage:
			p.resetAll()
			return true
		}
	}
	return false
}

// account counts n bytes in direction d. It returns how many of them to pass
// on, whether to drop them all (a blackhole), and the fault if it fires after
// the allowed bytes, with the channel that Heal closes.
func (p *Proxy) account(d Dir, n int) (allow int, swallow bool, act *Fault, healed <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.blackhole {
		return 0, true, nil, nil
	}
	f := p.fault
	if f == nil || p.fired || f.Dir != d {
		return n, false, nil, nil
	}
	left := f.At - p.count[d]
	if int64(n) < left {
		p.count[d] += int64(n)
		return n, false, nil, nil
	}
	p.count[d] = f.At
	p.fired = true
	close(p.firedCh)
	switch f.Action {
	case Blackhole:
		p.blackhole = true
	case Outage:
		p.outage = true
		if f.For > 0 {
			gen := p.gen
			time.AfterFunc(f.For, func() { p.heal(gen) })
		}
	}
	return int(max(left, 0)), false, f, p.healCh
}

// pair is one client connection and its connection to the target.
type pair struct {
	client, target net.Conn
	closing        atomic.Bool
	dead           atomic.Bool // swallows everything, forever
	once           sync.Once
}

// shut closes both ends. Safe to call more than once.
func (pr *pair) shut() {
	pr.once.Do(func() {
		pr.client.Close()
		pr.target.Close()
	})
}

// reset ends both ends with a TCP reset.
func (pr *pair) reset() {
	pr.once.Do(func() {
		resetConn(pr.client)
		resetConn(pr.target)
	})
}

// closeClean tells both ends the stream is over, then gives them a moment to
// stop sending before the forwarders close for good.
func (pr *pair) closeClean() {
	pr.closing.Store(true)
	for _, c := range []net.Conn{pr.client, pr.target} {
		closeWrite(c)
		_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	}
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

func resetConn(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	c.Close()
}
