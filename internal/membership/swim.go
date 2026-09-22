// Package membership implements SWIM failure detection, in-memory only.
package membership

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"sporemachine/internal/rpcclient"
	"sporemachine/internal/sporepb"
)

const (
	pingInterval    = 5 * time.Second
	pingTimeout     = 2 * time.Second
	suspectTimeout  = 30 * time.Second
	indirectPingers = 3
)

type entry struct {
	status      sporepb.MemberStatus
	incarnation int64
	suspectAt   time.Time
}

func severity(s sporepb.MemberStatus) int {
	switch s {
	case sporepb.MemberStatus_ALIVE:
		return 0
	case sporepb.MemberStatus_SUSPECT:
		return 1
	default:
		return 2
	}
}

type Membership struct {
	selfAddress string

	mu              sync.Mutex
	members         map[string]*entry
	selfIncarnation int64
}

func New(selfAddress string, seedContacts []string) *Membership {
	m := &Membership{
		selfAddress: selfAddress,
		members:     make(map[string]*entry),
	}
	for _, addr := range seedContacts {
		if addr == "" || addr == selfAddress {
			continue
		}
		m.members[addr] = &entry{status: sporepb.MemberStatus_ALIVE}
	}
	return m
}

func (m *Membership) Run(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

func (m *Membership) tick(ctx context.Context) {
	target, ok := m.randomMember(func(e *entry) bool { return e.status != sporepb.MemberStatus_DEAD })
	if ok {
		m.probe(ctx, target)
	}
	m.expireSuspects()
}

func (m *Membership) probe(ctx context.Context, target string) {
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	resp, err := m.callPing(pingCtx, target)
	cancel()
	if err == nil {
		m.markAlive(target)
		m.applyPiggyback(resp.GetPiggyback())
		return
	}

	if m.indirectProbe(ctx, target) {
		m.markAlive(target)
		return
	}
	m.markSuspect(target)
}

func (m *Membership) callPing(ctx context.Context, addr string) (*sporepb.PingResponse, error) {
	client, conn, err := rpcclient.Dial(addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return client.Ping(ctx, &sporepb.PingRequest{
		FromAddress: m.selfAddress,
		Piggyback:   m.snapshotPiggyback(),
	})
}

func (m *Membership) indirectProbe(ctx context.Context, target string) bool {
	mediators := m.randomMembers(indirectPingers, func(addr string, e *entry) bool {
		return addr != target && e.status != sporepb.MemberStatus_DEAD
	})
	if len(mediators) == 0 {
		return false
	}

	results := make(chan bool, len(mediators))
	var wg sync.WaitGroup
	for _, addr := range mediators {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
			defer cancel()
			client, conn, err := rpcclient.Dial(addr)
			if err != nil {
				results <- false
				return
			}
			defer conn.Close()
			resp, err := client.PingIndirect(pingCtx, &sporepb.IndirectPingRequest{TargetAddress: target})
			results <- err == nil && resp.GetAcked()
		}(addr)
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	for ok := range results {
		if ok {
			return true
		}
	}
	return false
}

func (m *Membership) HandlePing(req *sporepb.PingRequest) *sporepb.PingResponse {
	m.markAlive(req.GetFromAddress())
	m.applyPiggyback(req.GetPiggyback())
	return &sporepb.PingResponse{Piggyback: m.snapshotPiggyback()}
}

func (m *Membership) HandleIndirectPing(ctx context.Context, target string) bool {
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	_, err := m.callPing(pingCtx, target)
	return err == nil
}

func (m *Membership) markAlive(addr string) {
	if addr == "" || addr == m.selfAddress {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.members[addr]
	if !ok {
		e = &entry{}
		m.members[addr] = e
	}
	e.status = sporepb.MemberStatus_ALIVE
	e.suspectAt = time.Time{}
}

func (m *Membership) markSuspect(addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.members[addr]
	if !ok || e.status == sporepb.MemberStatus_DEAD {
		return
	}
	if e.status != sporepb.MemberStatus_SUSPECT {
		e.status = sporepb.MemberStatus_SUSPECT
		e.suspectAt = time.Now()
	}
}

func (m *Membership) expireSuspects() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, e := range m.members {
		if e.status == sporepb.MemberStatus_SUSPECT && !e.suspectAt.IsZero() && now.Sub(e.suspectAt) > suspectTimeout {
			e.status = sporepb.MemberStatus_DEAD
		}
	}
}

// applyPiggyback merges incoming gossip (higher incarnation wins; equal
// incarnation, more severe status wins) and bumps our own incarnation to
// refute a suspicion about ourselves.
func (m *Membership) applyPiggyback(updates []*sporepb.MemberState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range updates {
		if u.GetAddress() == m.selfAddress {
			if u.GetStatus() != sporepb.MemberStatus_ALIVE && u.GetIncarnation() >= m.selfIncarnation {
				m.selfIncarnation = u.GetIncarnation() + 1
			}
			continue
		}
		e, ok := m.members[u.GetAddress()]
		if !ok {
			m.members[u.GetAddress()] = &entry{status: u.GetStatus(), incarnation: u.GetIncarnation()}
			continue
		}
		if u.GetIncarnation() > e.incarnation ||
			(u.GetIncarnation() == e.incarnation && severity(u.GetStatus()) > severity(e.status)) {
			e.status = u.GetStatus()
			e.incarnation = u.GetIncarnation()
			if e.status != sporepb.MemberStatus_SUSPECT {
				e.suspectAt = time.Time{}
			}
		}
	}
}

func (m *Membership) snapshotPiggyback() []*sporepb.MemberState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*sporepb.MemberState, 0, len(m.members)+1)
	out = append(out, &sporepb.MemberState{
		Address:     m.selfAddress,
		Status:      sporepb.MemberStatus_ALIVE,
		Incarnation: m.selfIncarnation,
	})
	for addr, e := range m.members {
		out = append(out, &sporepb.MemberState{Address: addr, Status: e.status, Incarnation: e.incarnation})
	}
	return out
}

// Learn adds previously unknown addresses as ALIVE (peer exchange).
func (m *Membership) Learn(addrs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, addr := range addrs {
		if addr == "" || addr == m.selfAddress {
			continue
		}
		if _, ok := m.members[addr]; !ok {
			m.members[addr] = &entry{status: sporepb.MemberStatus_ALIVE}
		}
	}
}

// IsAlive reports whether addr is currently known and ALIVE.
func (m *Membership) IsAlive(addr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.members[addr]
	return ok && e.status == sporepb.MemberStatus_ALIVE
}

type Counts struct {
	Alive, Suspect, Dead int64
}

func (m *Membership) Counts() Counts {
	m.mu.Lock()
	defer m.mu.Unlock()
	var c Counts
	for _, e := range m.members {
		switch e.status {
		case sporepb.MemberStatus_ALIVE:
			c.Alive++
		case sporepb.MemberStatus_SUSPECT:
			c.Suspect++
		case sporepb.MemberStatus_DEAD:
			c.Dead++
		}
	}
	return c
}

func (m *Membership) AllAlivePeers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for addr, e := range m.members {
		if e.status == sporepb.MemberStatus_ALIVE {
			out = append(out, addr)
		}
	}
	return out
}

func (m *Membership) RandomAlivePeers(n int) []string {
	return m.randomMembers(n, func(_ string, e *entry) bool { return e.status == sporepb.MemberStatus_ALIVE })
}

func (m *Membership) randomMember(pred func(*entry) bool) (string, bool) {
	all := m.randomMembers(1, func(_ string, e *entry) bool { return pred(e) })
	if len(all) == 0 {
		return "", false
	}
	return all[0], true
}

func (m *Membership) randomMembers(n int, pred func(string, *entry) bool) []string {
	m.mu.Lock()
	candidates := make([]string, 0, len(m.members))
	for addr, e := range m.members {
		if pred(addr, e) {
			candidates = append(candidates, addr)
		}
	}
	m.mu.Unlock()

	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	if n > len(candidates) {
		n = len(candidates)
	}
	return candidates[:n]
}
