// Lesson: phases/06-replication-and-consensus/10-chain-and-primary-backup/docs/en.md
// Chain Replication and Primary-Backup
// Run:  go run .
// Test: go test ./...
// Sources: van Renesse & Schneider, "Chain Replication for Supporting High Throughput and Availability" (OSDI 2004), sections 2-3;
// Alsberg & Day, "A Principle for Resilient Sharing of Distributed Resources" (ICSE 1976) for primary-backup;
// Budhiraja, Marzullo, Schneider, Toueg, "The Primary-Backup Approach" (1993).
// Go 1.22+, standard library only. Deterministic simulated network; a simulated master plays the failure detector and reconfigurer.
package main

import (
	"container/heap"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------------------------- money (Phase 10, Lesson 01)

type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, fmt.Errorf("currency code must be three letters: %q", code)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("exponent must be 0..4: %d", exponent)
	}
	return Currency{code, exponent}, nil
}

func (c Currency) MinorUnitsPerMajor() int64 {
	s := int64(1)
	for i := 0; i < c.Exponent; i++ {
		s *= 10
	}
	return s
}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("overflow")
)

type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	sum := m.MinorUnits + other.MinorUnits
	if (m.MinorUnits > 0 && other.MinorUnits > 0 && sum < 0) || (m.MinorUnits < 0 && other.MinorUnits < 0 && sum >= 0) {
		return Money{}, ErrOverflow
	}
	return Money{sum, m.Currency}, nil
}

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, abs, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// fmtBalances prints a balance map the way Java prints a TreeMap: "{alice=74.50 USD}".
func fmtBalances(m map[string]Money) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k].String()
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func fmtInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ---------------------------------------------------------------------------------------------- deterministic infrastructure

// Rng is SplitMix64: the same stream for the same seed in every language.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng { return &Rng{state: uint64(seed)} }

func (r *Rng) NextLong() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *Rng) NextInt(bound int) int { return int((r.NextLong() >> 33) % uint64(bound)) }

// Request is one client request: a balance change (UPDATE) or a read (QUERY). (ClientID, ReqID) identifies it for retries and deduplication.
type Request struct {
	ClientID int
	ReqID    int64
	IsQuery  bool
	Account  string
	Delta    Money
}

func (r Request) Key() string { return fmt.Sprintf("%d:%d", r.ClientID, r.ReqID) }

func (r Request) String() string {
	if r.IsQuery {
		return fmt.Sprintf("Q%s(%s)", r.Key(), r.Account)
	}
	return fmt.Sprintf("U%s(%s,%s)", r.Key(), r.Account, r.Delta)
}

// Update is an update as it lives in a replica's history: the request plus the sequence number the head assigned.
type Update struct {
	Seq     int64
	Request Request
}

// historyReceived is the ack with seq -1 that a new tail sends when it has the old tail's history (Java: new Update(-1, null)).
var historyReceived = Update{Seq: -1}

type MessageKind int

const (
	UpdateMsg MessageKind = iota
	Ack
	Query
	Reply
	NotTail
	Sync
	StateTransfer
	SetLinks
	PBUpdate
	PBAck
	PBSync
)

func (k MessageKind) String() string {
	return [...]string{"UPDATE", "ACK", "QUERY", "REPLY", "NOT_TAIL", "SYNC", "STATE_TRANSFER", "SET_LINKS", "PB_UPDATE", "PB_ACK", "PB_SYNC"}[k]
}

// Message is one message on the wire. Fields are used per kind; unused ones are zero values (nil/null in Java).
type Message struct {
	Kind        MessageKind
	From, To    int
	Update      Update
	Updates     []Update
	Request     Request
	Balance     Money
	Predecessor int
	Successor   int
	IsHead      bool
	IsTail      bool
	Note        string
}

func updateMsg(from, to int, u Update) Message {
	return Message{Kind: UpdateMsg, From: from, To: to, Update: u}
}
func ack(from, to int, u Update) Message { return Message{Kind: Ack, From: from, To: to, Update: u} }
func query(from, to int, r Request) Message {
	return Message{Kind: Query, From: from, To: to, Request: r}
}
func reply(from, to int, r Request, balance Money) Message {
	return Message{Kind: Reply, From: from, To: to, Request: r, Balance: balance}
}
func notTail(from, to int, r Request) Message {
	return Message{Kind: NotTail, From: from, To: to, Request: r}
}
func syncMsg(from, to int, us []Update) Message {
	return Message{Kind: Sync, From: from, To: to, Updates: append([]Update(nil), us...)}
}
func stateTransfer(from, to int, history []Update) Message {
	return Message{Kind: StateTransfer, From: from, To: to, Updates: append([]Update(nil), history...)}
}
func setLinks(to, pred, succ int, note string) Message {
	return Message{Kind: SetLinks, From: -1, To: to, Predecessor: pred, Successor: succ, IsHead: pred < 0, IsTail: succ < 0, Note: note}
}
func pbUpdate(from, to int, u Update) Message {
	return Message{Kind: PBUpdate, From: from, To: to, Update: u}
}
func pbAck(from, to int, u Update) Message {
	return Message{Kind: PBAck, From: from, To: to, Update: u}
}
func pbSync(from, to int, history []Update) Message {
	return Message{Kind: PBSync, From: from, To: to, Updates: append([]Update(nil), history...)}
}

type Node interface {
	ID() int
	Handle(m Message, now int) []Message
}

type envelope struct {
	deliverAt int
	seq       int64
	message   Message
}

type envelopeHeap []envelope

func (h envelopeHeap) Len() int { return len(h) }
func (h envelopeHeap) Less(i, j int) bool {
	if h[i].deliverAt != h[j].deliverAt {
		return h[i].deliverAt < h[j].deliverAt
	}
	return h[i].seq < h[j].seq
}
func (h envelopeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *envelopeHeap) Push(x interface{}) { *h = append(*h, x.(envelope)) }
func (h *envelopeHeap) Pop() interface{} {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

// Network: seeded delays, crashes, FIFO per (from, to) pair. Chain replication assumes reliable FIFO links, so there are no drops.
type Network struct {
	rng                 *Rng
	maxDelay            int
	nodes               map[int]Node
	inFlight            envelopeHeap
	lastDeliveryPerLink map[[2]int]int
	down                map[int]bool
	observer            func()
	now                 int
	seq                 int64
	Sent, Delivered     int
	Dropped             int
}

func NewNetwork(seed int64, maxDelay int) *Network {
	if maxDelay < 1 {
		maxDelay = 1
	}
	return &Network{rng: NewRng(seed), maxDelay: maxDelay, nodes: map[int]Node{}, lastDeliveryPerLink: map[[2]int]int{},
		down: map[int]bool{}, observer: func() {}}
}

func (w *Network) Add(n Node)           { w.nodes[n.ID()] = n }
func (w *Network) SetObserver(f func()) { w.observer = f }
func (w *Network) Now() int             { return w.now }
func (w *Network) IsDown(id int) bool   { return w.down[id] }
func (w *Network) Crash(id int)         { w.down[id] = true }
func (w *Network) Idle() bool           { return len(w.inFlight) == 0 }

// Send is FIFO per link: a message never arrives before an earlier one on the same (from, to) pair, which the chain protocol relies on.
func (w *Network) Send(m Message) {
	w.Sent++
	if w.down[m.To] || (m.From >= 0 && w.down[m.From]) {
		w.Dropped++
		return
	}
	link := [2]int{m.From, m.To}
	earliest := w.now + 1
	if last := w.lastDeliveryPerLink[link]; last > earliest {
		earliest = last
	}
	at := earliest + w.rng.NextInt(w.maxDelay)
	w.lastDeliveryPerLink[link] = at
	heap.Push(&w.inFlight, envelope{at, w.seq, m})
	w.seq++
}

func (w *Network) SendAll(ms []Message) {
	for _, m := range ms {
		w.Send(m)
	}
}

func (w *Network) Tick() {
	w.now++
	for len(w.inFlight) > 0 && w.inFlight[0].deliverAt <= w.now {
		m := heap.Pop(&w.inFlight).(envelope).message
		if w.down[m.To] {
			w.Dropped++
			continue
		}
		w.Delivered++
		w.SendAll(w.nodes[m.To].Handle(m, w.now))
		w.observer()
	}
}

// orderedUpdates is Java's LinkedHashMap<Long, Update>: a map plus the insertion order, which a SYNC must preserve.
type orderedUpdates struct {
	keys []int64
	m    map[int64]Update
}

func newOrderedUpdates() *orderedUpdates { return &orderedUpdates{m: map[int64]Update{}} }

func (o *orderedUpdates) Put(u Update) {
	if _, ok := o.m[u.Seq]; !ok {
		o.keys = append(o.keys, u.Seq)
	}
	o.m[u.Seq] = u
}

func (o *orderedUpdates) Remove(seq int64) bool {
	if _, ok := o.m[seq]; !ok {
		return false
	}
	delete(o.m, seq)
	for i, k := range o.keys {
		if k == seq {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

func (o *orderedUpdates) Contains(seq int64) bool { _, ok := o.m[seq]; return ok }
func (o *orderedUpdates) Len() int                { return len(o.keys) }
func (o *orderedUpdates) Clear()                  { o.keys = nil; o.m = map[int64]Update{} }

func (o *orderedUpdates) Values() []Update {
	out := make([]Update, 0, len(o.keys))
	for _, k := range o.keys {
		out = append(out, o.m[k])
	}
	return out
}

// ---------------------------------------------------------------------------------------------- chain replication (van Renesse & Schneider)

// ChainNode is one replica in a chain. State: account balances, the history of applied updates (Hist_i), and the updates
// forwarded to the successor but not yet acknowledged by the tail (Sent_i). The two invariants the paper proves and this lesson checks:
//
//	Update Propagation: for i before j in the chain, Hist_j is a prefix of Hist_i.
//	Inprocess Requests:  Hist_i minus Hist_(i+1) is contained in Sent_i.
type ChainNode struct {
	id           int
	Predecessor  int
	Successor    int
	Balances     map[string]Money
	History      []Update
	seqsSeen     map[int64]bool
	requestsSeen map[string]bool
	Sent         *orderedUpdates
	// replyValues is the balance each update's reply carried, so a retried request that is already committed can be answered without re-applying.
	replyValues map[string]Money
	// TransferPending is true on an old tail while its history is still being copied to a new tail: it keeps answering as the tail until then.
	TransferPending bool
	// InChain is false for a spare or a joining replica whose history has not arrived yet; such a node is not part of the chain's invariants.
	InChain                                                      bool
	NextSeq                                                      int64
	UpdatesHandled, QueriesHandled, AcksHandled, MessagesHandled int
}

func NewChainNode(id int) *ChainNode {
	return &ChainNode{id: id, Predecessor: -1, Successor: -1, Balances: map[string]Money{}, seqsSeen: map[int64]bool{},
		requestsSeen: map[string]bool{}, Sent: newOrderedUpdates(), replyValues: map[string]Money{}, NextSeq: 1}
}

func (n *ChainNode) ID() int      { return n.id }
func (n *ChainNode) IsHead() bool { return n.Predecessor < 0 }
func (n *ChainNode) IsTail() bool { return n.Successor < 0 }

// ActsAsTail: who answers clients: the real tail, or an old tail whose successor has not finished receiving the history yet.
func (n *ChainNode) ActsAsTail() bool { return n.IsTail() || n.TransferPending }

func (n *ChainNode) Balance(account string) Money {
	if b, ok := n.Balances[account]; ok {
		return b
	}
	return OfMinor(0, USD)
}

func mustPlus(a, b Money) Money {
	sum, err := a.Plus(b)
	if err != nil {
		panic(err)
	}
	return sum
}

func (n *ChainNode) apply(u Update) {
	n.History = append(n.History, u)
	n.seqsSeen[u.Seq] = true
	n.requestsSeen[u.Request.Key()] = true
	n.Balances[u.Request.Account] = mustPlus(n.Balance(u.Request.Account), u.Request.Delta)
	n.replyValues[u.Request.Key()] = n.Balance(u.Request.Account)
	if u.Seq+1 > n.NextSeq {
		n.NextSeq = u.Seq + 1
	}
}

func (n *ChainNode) Handle(m Message, now int) []Message {
	n.MessagesHandled++
	out := []Message{}
	switch m.Kind {
	case UpdateMsg:
		n.UpdatesHandled++
		u := m.Update
		if m.From >= 100 && !n.IsHead() { // stale client view: only the head accepts updates; the client retries
			return out
		}
		if n.IsHead() && m.From >= 100 { // from a client: assign the sequence number
			if n.requestsSeen[u.Request.Key()] { // duplicate retry: never apply twice
				stillInFlight := false
				for _, s := range n.Sent.Values() {
					if s.Request.Key() == u.Request.Key() {
						stillInFlight = true
					}
				}
				if !stillInFlight { // committed: answer from the cache
					out = append(out, reply(n.id, m.From, u.Request, n.replyValues[u.Request.Key()]))
				}
				return out // otherwise the tail's reply is on its way
			}
			u = Update{n.NextSeq, u.Request}
		}
		if n.seqsSeen[u.Seq] { // already have it (resent after a reconfiguration)
			return out
		}
		n.apply(u)
		out = append(out, n.forwardOrCommit(u)...)
	case Ack:
		n.AcksHandled++
		if m.Update.Seq < 0 { // the new tail has the history: hand over
			n.TransferPending = false
			return out
		}
		if n.Sent.Remove(m.Update.Seq) && !n.IsHead() {
			out = append(out, ack(n.id, n.Predecessor, m.Update))
		}
	case Query:
		n.QueriesHandled++
		if n.ActsAsTail() {
			out = append(out, reply(n.id, m.From, m.Request, n.Balance(m.Request.Account)))
		} else {
			out = append(out, notTail(n.id, m.From, m.Request)) // a stale client view; it must re-ask the master
		}
	case Sync: // predecessor resends Sent after a reconfiguration
		for _, u := range m.Updates {
			if n.seqsSeen[u.Seq] { // we have it; the ack may have been lost with the dead node
				out = append(out, ack(n.id, m.From, u))
				continue
			}
			n.apply(u)
			out = append(out, n.forwardOrCommit(u)...)
		}
	case StateTransfer: // joining at the tail: adopt the old tail's history
		for _, u := range m.Updates {
			if !n.seqsSeen[u.Seq] {
				n.apply(u)
			}
		}
		n.InChain = true
		out = append(out, ack(n.id, m.From, historyReceived)) // seq -1: "history received", the old tail stops acting as tail
	case SetLinks:
		wasTail := n.IsTail()
		n.Predecessor = m.Predecessor
		n.Successor = m.Successor
		n.InChain = m.Note != "extend-new" // a joining tail is in the chain once its history arrives
		if n.IsTail() && !wasTail {        // the tail failed: everything we had in flight is now committed
			for _, u := range n.Sent.Values() {
				out = append(out, reply(n.id, u.Request.ClientID, u.Request, n.Balance(u.Request.Account)))
				if !n.IsHead() {
					out = append(out, ack(n.id, n.Predecessor, u))
				}
			}
			n.Sent.Clear()
		} else if !n.IsTail() { // new successor: resend what it may be missing
			if wasTail && strings.HasPrefix(m.Note, "extend") {
				n.TransferPending = true // keep answering clients until the copy lands
				out = append(out, stateTransfer(n.id, n.Successor, n.History))
			} else if n.Sent.Len() > 0 {
				out = append(out, syncMsg(n.id, n.Successor, n.Sent.Values()))
			}
		}
	default:
		panic(fmt.Sprintf("chain node got %s", m.Kind))
	}
	return out
}

func (n *ChainNode) forwardOrCommit(u Update) []Message {
	out := []Message{}
	if n.ActsAsTail() {
		out = append(out, reply(n.id, u.Request.ClientID, u.Request, n.Balance(u.Request.Account)))
		if n.IsTail() && !n.IsHead() {
			out = append(out, ack(n.id, n.Predecessor, u))
		}
	}
	if !n.IsTail() {
		n.Sent.Put(u)
		out = append(out, updateMsg(n.id, n.Successor, u))
	}
	return out
}

// Master is a reliable (in the paper, itself replicated) service that detects failures and tells nodes who their
// neighbours are. Here it is a plain object the test drives: Fail(id), Extend(id). Clients ask it for the head and tail.
type Master struct {
	Chain            []int
	Network          *Network
	Events           []string
	Reconfigurations int
}

func NewMaster(network *Network, initialChain []int) *Master {
	return &Master{Chain: append([]int(nil), initialChain...), Network: network}
}

func (m *Master) Head() int   { return m.Chain[0] }
func (m *Master) Tail() int   { return m.Chain[len(m.Chain)-1] }
func (m *Master) Length() int { return len(m.Chain) }

func (m *Master) pred(i int) int {
	if i == 0 {
		return -1
	}
	return m.Chain[i-1]
}

func (m *Master) succ(i int) int {
	if i == len(m.Chain)-1 {
		return -1
	}
	return m.Chain[i+1]
}

// PublishLinks pushes every node's links. Deterministic order: tail first so a new tail starts replying before its predecessor forwards.
func (m *Master) PublishLinks(note string) {
	m.Reconfigurations++
	for i := len(m.Chain) - 1; i >= 0; i-- {
		n := note
		if note == "extend" && i == len(m.Chain)-1 {
			n = "extend-new"
		}
		m.Network.Send(setLinks(m.Chain[i], m.pred(i), m.succ(i), n))
	}
}

// Fail: the failure detector fired for `id`. Section 3 of the paper: head, tail, and middle failures each need a different repair.
func (m *Master) Fail(id int) {
	i := -1
	for k, n := range m.Chain {
		if n == id {
			i = k
			break
		}
	}
	if i < 0 {
		return
	}
	kind := "middle"
	if i == 0 {
		kind = "head"
	} else if i == len(m.Chain)-1 {
		kind = "tail"
	}
	m.Chain = append(m.Chain[:i], m.Chain[i+1:]...)
	if len(m.Chain) == 0 {
		panic("chain has no replicas left")
	}
	m.Events = append(m.Events, fmt.Sprintf("t=%-4d master: n%d (%s) failed -> chain %s", m.Network.Now(), id, kind, fmtInts(m.Chain)))
	m.PublishLinks("fail " + kind)
}

// Extend adds a fresh replica at the tail. The old tail transfers its history; until the transfer lands the old tail still answers queries.
func (m *Master) Extend(id int) {
	m.Chain = append(m.Chain, id)
	m.Events = append(m.Events, fmt.Sprintf("t=%-4d master: extend chain with n%d at the tail -> %s", m.Network.Now(), id, fmtInts(m.Chain)))
	m.PublishLinks("extend")
}

// ChainClient sends updates to the head and queries to the tail, as the master currently reports them; retries on timeout.
type ChainClient struct {
	id                        int
	Master                    *Master
	pendingSentAt             map[string]int
	pendingOrder              []string // Java's LinkedHashMap order: retries are issued in the order requests were made
	Pending                   map[string]Request
	Replies                   map[string]Money
	RepliesInOrder            []string
	Retries, NotTailRedirects int
	nextReq                   int64
}

func NewChainClient(id int, master *Master) *ChainClient {
	return &ChainClient{id: id, Master: master, pendingSentAt: map[string]int{}, Pending: map[string]Request{}, Replies: map[string]Money{}, nextReq: 1}
}

func (c *ChainClient) ID() int { return c.id }

func (c *ChainClient) track(r Request, now int) {
	c.Pending[r.Key()] = r
	c.pendingOrder = append(c.pendingOrder, r.Key())
	c.pendingSentAt[r.Key()] = now
}

// Update issues an update to the current head. Returns the request and the message(s) to send (the Java appends to an out list).
func (c *ChainClient) Update(account string, delta Money, now int) (Request, []Message) {
	r := Request{c.id, c.nextReq, false, account, delta}
	c.nextReq++
	c.track(r, now)
	return r, []Message{updateMsg(c.id, c.Master.Head(), Update{0, r})}
}

func (c *ChainClient) Query(account string, now int) (Request, []Message) {
	r := Request{c.id, c.nextReq, true, account, Money{}}
	c.nextReq++
	c.track(r, now)
	return r, []Message{query(c.id, c.Master.Tail(), r)}
}

// RetryStale resends anything outstanding longer than `timeout`, to whoever the master now says is head/tail.
func (c *ChainClient) RetryStale(now, timeout int) []Message {
	out := []Message{}
	for _, key := range append([]string(nil), c.pendingOrder...) {
		r, ok := c.Pending[key]
		if !ok {
			continue
		}
		if now-c.pendingSentAt[key] >= timeout {
			c.Retries++
			c.pendingSentAt[key] = now
			if r.IsQuery {
				out = append(out, query(c.id, c.Master.Tail(), r))
			} else {
				out = append(out, updateMsg(c.id, c.Master.Head(), Update{0, r}))
			}
		}
	}
	return out
}

func (c *ChainClient) Handle(m Message, now int) []Message {
	out := []Message{}
	switch m.Kind {
	case Reply:
		key := m.Request.Key()
		if _, ok := c.Pending[key]; ok { // duplicates after a tail failover are ignored
			delete(c.Pending, key)
			for i, k := range c.pendingOrder {
				if k == key {
					c.pendingOrder = append(c.pendingOrder[:i], c.pendingOrder[i+1:]...)
					break
				}
			}
			c.Replies[key] = m.Balance
			c.RepliesInOrder = append(c.RepliesInOrder, key)
			delete(c.pendingSentAt, key)
		}
	case NotTail:
		c.NotTailRedirects++
		out = append(out, query(c.id, c.Master.Tail(), m.Request))
	default:
		panic(fmt.Sprintf("client got %s", m.Kind))
	}
	return out
}

func (c *ChainClient) HasReply(r Request) bool { _, ok := c.Replies[r.Key()]; return ok }

// ReplyFor returns the balance the reply carried and whether a reply has arrived (Java returns null before that).
func (c *ChainClient) ReplyFor(r Request) (Money, bool) {
	m, ok := c.Replies[r.Key()]
	return m, ok
}

// ---------------------------------------------------------------------------------------------- primary-backup, for comparison

// PrimaryBackupNode is primary-backup with synchronous replication: the primary applies an update, sends it to every backup,
// waits for all acks, then replies. Queries go to the primary. On promotion the new primary pushes its history to the
// backups, because the old primary may have reached some backups and not others with its last updates.
type PrimaryBackupNode struct {
	id                                                           int
	IsPrimary                                                    bool
	Backups                                                      []int
	Balances                                                     map[string]Money
	History                                                      []Update
	seqsSeen                                                     map[int64]bool
	requestsSeen                                                 map[string]bool
	awaitingAcks                                                 map[int64]map[int]bool
	awaitingUpdate                                               map[int64]Update
	NextSeq                                                      int64
	UpdatesHandled, QueriesHandled, AcksHandled, MessagesHandled int
}

func NewPrimaryBackupNode(id int) *PrimaryBackupNode {
	return &PrimaryBackupNode{id: id, Balances: map[string]Money{}, seqsSeen: map[int64]bool{}, requestsSeen: map[string]bool{},
		awaitingAcks: map[int64]map[int]bool{}, awaitingUpdate: map[int64]Update{}, NextSeq: 1}
}

func (n *PrimaryBackupNode) ID() int { return n.id }

func (n *PrimaryBackupNode) Balance(account string) Money {
	if b, ok := n.Balances[account]; ok {
		return b
	}
	return OfMinor(0, USD)
}

func (n *PrimaryBackupNode) apply(u Update) {
	n.History = append(n.History, u)
	n.seqsSeen[u.Seq] = true
	n.requestsSeen[u.Request.Key()] = true
	n.Balances[u.Request.Account] = mustPlus(n.Balance(u.Request.Account), u.Request.Delta)
	if u.Seq+1 > n.NextSeq {
		n.NextSeq = u.Seq + 1
	}
}

func (n *PrimaryBackupNode) Handle(m Message, now int) []Message {
	n.MessagesHandled++
	out := []Message{}
	switch m.Kind {
	case UpdateMsg: // from a client
		n.UpdatesHandled++
		if !n.IsPrimary { // stale client view: silence, the client retries
			return out
		}
		if n.requestsSeen[m.Update.Request.Key()] {
			return append(out, reply(n.id, m.From, m.Update.Request, n.Balance(m.Update.Request.Account)))
		}
		u := Update{n.NextSeq, m.Update.Request}
		n.apply(u)
		if len(n.Backups) == 0 {
			return append(out, reply(n.id, m.From, u.Request, n.Balance(u.Request.Account)))
		}
		waiting := map[int]bool{}
		for _, b := range n.Backups {
			waiting[b] = true
		}
		n.awaitingAcks[u.Seq] = waiting
		n.awaitingUpdate[u.Seq] = u
		for _, b := range n.Backups {
			out = append(out, pbUpdate(n.id, b, u))
		}
	case PBUpdate:
		n.UpdatesHandled++
		if !n.seqsSeen[m.Update.Seq] {
			n.apply(m.Update)
		}
		out = append(out, pbAck(n.id, m.From, m.Update))
	case PBAck:
		n.AcksHandled++
		waiting, ok := n.awaitingAcks[m.Update.Seq]
		if !ok {
			return out
		}
		delete(waiting, m.From)
		if len(waiting) == 0 {
			delete(n.awaitingAcks, m.Update.Seq)
			u := n.awaitingUpdate[m.Update.Seq]
			delete(n.awaitingUpdate, m.Update.Seq)
			out = append(out, reply(n.id, u.Request.ClientID, u.Request, n.Balance(u.Request.Account)))
		}
	case Query:
		n.QueriesHandled++
		if n.IsPrimary {
			out = append(out, reply(n.id, m.From, m.Request, n.Balance(m.Request.Account)))
		} else {
			out = append(out, notTail(n.id, m.From, m.Request))
		}
	case PBSync:
		for _, u := range m.Updates {
			if !n.seqsSeen[u.Seq] {
				n.apply(u)
			}
		}
		out = append(out, pbAck(n.id, m.From, historyReceived))
	default:
		panic(fmt.Sprintf("primary-backup node got %s", m.Kind))
	}
	return out
}

// Promote is called by the master on promotion: become primary over `newBackups` and re-sync them from my history.
func (n *PrimaryBackupNode) Promote(newBackups []int) []Message {
	n.IsPrimary = true
	n.Backups = append([]int(nil), newBackups...)
	out := []Message{}
	for _, b := range n.Backups {
		out = append(out, pbSync(n.id, b, n.History))
	}
	return out
}

// PrimaryBackupMaster holds the member list for the primary-backup group and promotes on failure.
type PrimaryBackupMaster struct {
	Members []int
	Network *Network
	Nodes   map[int]*PrimaryBackupNode
	Events  []string
}

func NewPrimaryBackupMaster(network *Network, nodes map[int]*PrimaryBackupNode, members []int) *PrimaryBackupMaster {
	m := &PrimaryBackupMaster{Members: append([]int(nil), members...), Network: network, Nodes: nodes}
	network.SendAll(nodes[m.Members[0]].Promote(m.Members[1:]))
	return m
}

func (m *PrimaryBackupMaster) Primary() int { return m.Members[0] }

func (m *PrimaryBackupMaster) Fail(id int) {
	i := -1
	for k, n := range m.Members {
		if n == id {
			i = k
			break
		}
	}
	if i < 0 {
		return
	}
	m.Members = append(m.Members[:i], m.Members[i+1:]...)
	m.Events = append(m.Events, fmt.Sprintf("t=%-4d pb-master: n%d failed -> primary n%d, backups %s", m.Network.Now(), id, m.Members[0], fmtInts(m.Members[1:])))
	m.Network.SendAll(m.Nodes[m.Members[0]].Promote(m.Members[1:]))
}

// ---------------------------------------------------------------------------------------------- the simulation harness

// ChainCluster is a chain, its master, its clients, and the invariant checker, on one network.
type ChainCluster struct {
	Network    *Network
	Master     *Master
	Nodes      map[int]*ChainNode
	Clients    map[int]*ChainClient
	clientIDs  []int
	Violations []string
	Checks     int
}

func NewChainCluster(seed int64, maxDelay, chainLength, spares, clientCount int) *ChainCluster {
	c := &ChainCluster{Network: NewNetwork(seed, maxDelay), Nodes: map[int]*ChainNode{}, Clients: map[int]*ChainClient{}}
	initial := []int{}
	for i := 0; i < chainLength+spares; i++ {
		n := NewChainNode(i)
		c.Nodes[i] = n
		c.Network.Add(n)
		if i < chainLength {
			initial = append(initial, i)
		}
	}
	c.Master = NewMaster(c.Network, initial)
	for k := 0; k < clientCount; k++ {
		cl := NewChainClient(100+k, c.Master)
		c.Clients[cl.id] = cl
		c.clientIDs = append(c.clientIDs, cl.id)
		c.Network.Add(cl)
	}
	c.Master.PublishLinks("initial")
	c.Network.SetObserver(c.CheckInvariants)
	c.Run(maxDelay + 2)
	return c
}

func (c *ChainCluster) Node(id int) *ChainNode    { return c.Nodes[id] }
func (c *ChainCluster) Client(i int) *ChainClient { return c.Clients[100+i] }
func (c *ChainCluster) Now() int                  { return c.Network.Now() }
func (c *ChainCluster) Tick()                     { c.Network.Tick() }
func (c *ChainCluster) Run(ticks int) {
	for i := 0; i < ticks; i++ {
		c.Tick()
	}
}

// RunUntilQuiet runs until no client has a pending request (retrying stale ones every `timeout` ticks) or maxTicks pass.
func (c *ChainCluster) RunUntilQuiet(maxTicks, timeout int) int {
	for i := 0; i < maxTicks; i++ {
		c.Tick()
		for _, id := range c.clientIDs {
			c.Network.SendAll(c.Clients[id].RetryStale(c.Now(), timeout))
		}
		quiet := c.Network.Idle()
		for _, id := range c.clientIDs {
			if len(c.Clients[id].Pending) > 0 {
				quiet = false
			}
		}
		if quiet {
			return c.Now()
		}
	}
	return -1
}

func (c *ChainCluster) Update(client int, account string, cents int64) Request {
	r, msgs := c.Client(client).Update(account, OfMinor(cents, USD), c.Now())
	c.Network.SendAll(msgs)
	return r
}

func (c *ChainCluster) Query(client int, account string) Request {
	r, msgs := c.Client(client).Query(account, c.Now())
	c.Network.SendAll(msgs)
	return r
}

func (c *ChainCluster) Crash(id int) { c.Network.Crash(id) }

// CrashAndDetect: the failure detector reports after `detectionDelay` ticks; until then the chain is broken and clients wait.
func (c *ChainCluster) CrashAndDetect(id, detectionDelay int) {
	c.Crash(id)
	c.Run(detectionDelay)
	c.Master.Fail(id)
}

// CheckInvariants checks Update Propagation and Inprocess Requests over the live chain, after every delivery.
func (c *ChainCluster) CheckInvariants() {
	c.Checks++
	chain := c.Master.Chain
	for i := 0; i+1 < len(chain); i++ {
		pred, succ := c.Nodes[chain[i]], c.Nodes[chain[i+1]]
		if c.Network.IsDown(pred.id) || c.Network.IsDown(succ.id) || pred.TransferPending || !succ.InChain { // a joining tail is not in the chain yet
			continue
		}
		if len(succ.History) > len(pred.History) {
			c.Violations = append(c.Violations, fmt.Sprintf("UPDATE PROPAGATION: n%d longer than n%d", succ.id, pred.id))
			return
		}
		for k := range succ.History {
			if succ.History[k] != pred.History[k] {
				c.Violations = append(c.Violations, fmt.Sprintf("UPDATE PROPAGATION: n%d differs from n%d at %d", succ.id, pred.id, k))
				return
			}
		}
		for k := len(succ.History); k < len(pred.History); k++ {
			u := pred.History[k]
			if !pred.Sent.Contains(u.Seq) {
				c.Violations = append(c.Violations, fmt.Sprintf("INPROCESS REQUESTS: n%d has seq %d that n%d lacks, and it is not in Sent", pred.id, u.Seq, succ.id))
				return
			}
		}
	}
}

func (c *ChainCluster) Tail() *ChainNode { return c.Nodes[c.Master.Tail()] }
func (c *ChainCluster) Head() *ChainNode { return c.Nodes[c.Master.Head()] }

// ---------------------------------------------------------------------------------------------- demo

func role(n *ChainNode) string {
	switch {
	case n.IsHead():
		return "HEAD"
	case n.IsTail():
		return "TAIL"
	default:
		return "middle"
	}
}

func printChain(c *ChainCluster) {
	for _, id := range c.Master.Chain {
		n := c.Node(id)
		fmt.Printf("  n%d %-6s hist=%d sent=%d balances=%s\n", id, role(n), len(n.History), n.Sent.Len(), fmtBalances(n.Balances))
	}
}

// replyText prints a reply the way Java prints a nullable Money: the value, or "null".
func replyText(c *ChainClient, r Request) string {
	if m, ok := c.ReplyFor(r); ok {
		return m.String()
	}
	return "null"
}

func totalReplies(c *ChainCluster) int {
	return len(c.Client(0).Replies) + len(c.Client(1).Replies) + len(c.Client(2).Replies)
}

func totalRetries(c *ChainCluster) int {
	return c.Client(0).Retries + c.Client(1).Retries + c.Client(2).Retries
}

func main() {
	fmt.Println("== A 3-replica chain: updates enter at the head, replies come from the tail (seed 1, delay 1-3) ==")
	c := NewChainCluster(1, 3, 3, 1, 2)
	u1 := c.Update(0, "alice", 10000)
	u2 := c.Update(0, "alice", -2550)
	q1 := c.Query(1, "alice") // sent at the same instant as the updates
	c.RunUntilQuiet(500, 60)
	fmt.Printf("client 0: Credit alice 100.00 -> reply %s; Debit 25.50 -> reply %s\n", replyText(c.Client(0), u1), replyText(c.Client(0), u2))
	fmt.Printf("client 1: Query alice issued at t=0 -> %s (the tail had not yet seen the updates: reads show only committed state)\n", replyText(c.Client(1), q1))
	q2 := c.Query(1, "alice")
	c.RunUntilQuiet(500, 60)
	fmt.Printf("client 1: Query alice after the replies -> %s\n", replyText(c.Client(1), q2))
	printChain(c)

	fmt.Println()
	fmt.Println("== A read at the tail never shows an update the head has applied but the tail has not ==")
	v := NewChainCluster(2, 8, 3, 0, 2)
	w := v.Update(0, "bob", 5000)
	for len(v.Head().History) == 0 { // the head has applied it; the tail has not
		v.Tick()
	}
	fmt.Printf("t=%d: head history=%d, tail history=%d, client reply yet? %v\n", v.Now(), len(v.Head().History), len(v.Tail().History), v.Client(0).HasReply(w))
	sentAt := v.Now()
	r := v.Query(1, "bob")
	v.RunUntilQuiet(500, 100)
	fmt.Printf("query sent at t=%d answered with %s; the update's own reply was %s -> a client never observes a write before the writer could\n",
		sentAt, replyText(v.Client(1), r), replyText(v.Client(0), w))

	fmt.Println()
	fmt.Println("== Failures: head, middle, tail, each repaired by the master; no committed update is lost ==")
	f := NewChainCluster(3, 3, 4, 2, 3)
	for i := 0; i < 6; i++ {
		f.Update(i%3, fmt.Sprintf("acct%d", i%2), int64(1000+i))
	}
	f.RunUntilQuiet(500, 60)
	fmt.Printf("6 updates committed; chain %s; tail history %d\n", fmtInts(f.Master.Chain), len(f.Tail().History))
	// middle failure while updates are in flight
	for i := 0; i < 4; i++ {
		f.Update(i%3, "acct0", 100)
	}
	f.Run(2)
	f.CrashAndDetect(f.Master.Chain[1], 20)
	f.RunUntilQuiet(1000, 60)
	fmt.Println(f.Master.Events[len(f.Master.Events)-1])
	fmt.Printf("  after middle failure: replies total=%d, tail history=%d, retries=%d\n", totalReplies(f), len(f.Tail().History), totalRetries(f))
	// tail failure with acks outstanding
	for i := 0; i < 3; i++ {
		f.Update(i%3, "acct1", 7)
	}
	f.Run(1)
	f.CrashAndDetect(f.Master.Tail(), 20)
	f.RunUntilQuiet(1000, 60)
	fmt.Println(f.Master.Events[len(f.Master.Events)-1])
	fmt.Printf("  after tail failure: the new tail replied for %d entries; outstanding Sent at new tail=%d\n", len(f.Tail().History), f.Tail().Sent.Len())
	// head failure: requests the head took but had not forwarded are lost and retried by clients
	for i := 0; i < 3; i++ {
		f.Update(i%3, "acct0", 3)
	}
	f.Run(1)
	f.CrashAndDetect(f.Master.Head(), 20)
	f.RunUntilQuiet(1000, 60)
	fmt.Println(f.Master.Events[len(f.Master.Events)-1])
	fmt.Printf("  after head failure: chain %s, every client request answered, retries total=%d\n", fmtInts(f.Master.Chain), totalRetries(f))
	// extend back to length 3
	f.Master.Extend(4)
	f.RunUntilQuiet(500, 60)
	f.Update(0, "acct0", 1)
	f.RunUntilQuiet(500, 60)
	fmt.Println(f.Master.Events[len(f.Master.Events)-1])
	printChain(f)
	fmt.Printf("  invariant checks run: %d, violations: %d\n", f.Checks, len(f.Violations))
	for _, x := range f.Violations {
		fmt.Println("  " + x)
	}

	fmt.Println()
	fmt.Println("== Load: chain spreads updates (head) and reads (tail) over different replicas; primary-backup puts both on one ==")
	updates, queries := 200, 1000
	lc := NewChainCluster(5, 2, 3, 0, 4)
	for i := 0; i < updates; i++ {
		lc.Update(i%4, fmt.Sprintf("a%d", i%10), 100)
	}
	for i := 0; i < queries; i++ {
		lc.Query(i%4, fmt.Sprintf("a%d", i%10))
	}
	lc.RunUntilQuiet(20000, 200)
	fmt.Printf("chain (3 replicas), %d updates + %d queries:\n", updates, queries)
	for _, id := range lc.Master.Chain {
		n := lc.Node(id)
		fmt.Printf("  n%d %-6s handled updates=%-4d queries=%-4d acks=%-4d total=%d\n", id, role(n), n.UpdatesHandled, n.QueriesHandled, n.AcksHandled, n.MessagesHandled)
	}
	pbNet := NewNetwork(5, 2)
	pbNodes := map[int]*PrimaryBackupNode{}
	for i := 0; i < 3; i++ {
		n := NewPrimaryBackupNode(i)
		pbNodes[i] = n
		pbNet.Add(n)
	}
	NewPrimaryBackupMaster(pbNet, pbNodes, []int{0, 1, 2})
	facade := NewMaster(pbNet, []int{0}) // clients ask "head" and "tail": both are the primary
	pbClients := []*ChainClient{}
	for i := 0; i < 4; i++ {
		cl := NewChainClient(100+i, facade)
		pbClients = append(pbClients, cl)
		pbNet.Add(cl)
	}
	for t := 0; t < 10; t++ {
		pbNet.Tick()
	}
	out := []Message{}
	for i := 0; i < updates; i++ {
		_, msgs := pbClients[i%4].Update(fmt.Sprintf("a%d", i%10), OfMinor(100, USD), pbNet.Now())
		out = append(out, msgs...)
	}
	for i := 0; i < queries; i++ {
		_, msgs := pbClients[i%4].Query(fmt.Sprintf("a%d", i%10), pbNet.Now())
		out = append(out, msgs...)
	}
	pbNet.SendAll(out)
	for !pbNet.Idle() {
		pbNet.Tick()
	}
	fmt.Println("primary-backup (1 primary + 2 backups), same workload:")
	for i := 0; i < 3; i++ {
		n := pbNodes[i]
		r := "backup"
		if n.IsPrimary {
			r = "PRIMARY"
		}
		fmt.Printf("  n%d %-7s handled updates=%-4d queries=%-4d acks=%-4d total=%d\n", n.id, r, n.UpdatesHandled, n.QueriesHandled, n.AcksHandled, n.MessagesHandled)
	}
	fmt.Printf("update latency: chain = one hop per replica plus the reply (%d hops serial); primary-backup = primary -> backups -> primary -> client (3 hops, backups in parallel)\n", lc.Master.Length())
	fmt.Println("read consistency: chain tail reads and primary reads are both linearizable; chain backups never serve reads, p/b backups never serve anything")
}
