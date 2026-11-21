// Package model 定义弹性光网络的核心数据模型：
// 节点、光纤链路、频隙网格、调制方式、业务连接与中继分段。
//
// 频谱约定：每段光纤有 Slots 个等宽频隙（12.5GHz 粒度），
// 一条连接的某个中继段在其经过的每条链路上占用 [Start, Start+Slots)
// 的连续频隙，相邻连接之间保留 GuardSlots 个保护频隙。
package model

import (
	"math"
	"sort"
)

// Modulation 一种调制格式。BitsPerSym 为每个符号承载的比特数，
// MaxReachKm 为该调制无电中继的最大传输距离。
type Modulation struct {
	Name       string
	BitsPerSym float64
	MaxReachKm float64
}

// Modulations 按调制效率从高到低排列。距离越远只能选越靠后的格式，
// 占用的频隙也就越多。
var Modulations = []Modulation{
	{Name: "16QAM", BitsPerSym: 4, MaxReachKm: 625},
	{Name: "8QAM", BitsPerSym: 3, MaxReachKm: 1250},
	{Name: "QPSK", BitsPerSym: 2, MaxReachKm: 2000},
	{Name: "BPSK", BitsPerSym: 1, MaxReachKm: 4000},
}

// SlotRateGbps 单个频隙在 BPSK（每符号 1 比特）下的净载荷速率。
const SlotRateGbps = 12.5

// SlotsNeeded 计算指定速率在指定调制下需要的频隙数（不含保护带）。
func SlotsNeeded(rateGbps int, m Modulation) int {
	return int(math.Ceil(float64(rateGbps) / (SlotRateGbps * m.BitsPerSym)))
}

// BestModulation 按距离选择可用的最高阶调制；距离超过 BPSK 极限时
// 返回 false，意味着该段必须拆分并加入电中继。
func BestModulation(distanceKm float64) (Modulation, bool) {
	for _, m := range Modulations {
		if distanceKm <= m.MaxReachKm {
			return m, true
		}
	}
	return Modulation{}, false
}

// Node 干线节点（城市/站点）。X、Y 为地理坐标，用于页面展示与距离计算。
type Node struct {
	ID   int
	Name string
	X, Y float64
}

// Interval 链路上一段已分配的连续频隙 [Start, End)。
type Interval struct {
	Start int
	End   int
	SegID int // 所属中继段 ID
}

// Link 一段双向光纤（两个方向各自独立计频隙，建模一份即可）。
type Link struct {
	ID       int
	U, V     int
	LengthKm float64
	Slots    int

	// data: 中继段 ID -> 数据频隙区间 [start,end)，保护带不登记，
	// 在可行性判断时按 GuardSlots 动态外扩。
	data map[int][2]int
}

// NewLink 构造一条空链路。
func NewLink(id, u, v int, lengthKm float64, slots int) *Link {
	return &Link{
		ID: id, U: u, V: v, LengthKm: lengthKm, Slots: slots,
		data: make(map[int][2]int),
	}
}

// OtherEnd 返回链路另一端的节点。
func (l *Link) OtherEnd(n int) int {
	if l.U == n {
		return l.V
	}
	return l.U
}

// UsedDataSlots 已被数据占用的频隙数（不含保护带）。
func (l *Link) UsedDataSlots() int {
	used := 0
	for _, iv := range l.data {
		used += iv[1] - iv[0]
	}
	return used
}

// FreeSlots 空闲频隙总数。
func (l *Link) FreeSlots() int { return l.Slots - l.UsedDataSlots() }

// Intervals 返回按起始频隙排序的已分配区间。
func (l *Link) Intervals() []Interval {
	out := make([]Interval, 0, len(l.data))
	for segID, iv := range l.data {
		out = append(out, Interval{Start: iv[0], End: iv[1], SegID: segID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// FeasibleStarts 返回能容纳 n 个连续频隙（含两侧保护带约束）的所有
// 合法起始位置。保护带通过把已有区间向两侧外扩 GuardSlots 后做
// 交集判定实现，因此保护带不需要登记归属，释放连接时也没有归属歧义。
func (l *Link) FeasibleStarts(n, guard int) []int {
	if n <= 0 || n > l.Slots {
		return nil
	}
	occupied := l.Intervals()
	var starts []int
	// cursor: 当前被外扩占用区挡住的最小位置
	cursor := 0
	for _, iv := range occupied {
		blockLo := iv.Start - guard
		blockHi := iv.End + guard // 排他上界
		if blockLo < 0 {
			blockLo = 0
		}
		if blockHi > l.Slots {
			blockHi = l.Slots
		}
		// cursor 与 blockLo 之间的空隙可以放 [s, s+n)
		if cursor+n <= blockLo {
			for s := cursor; s+n <= blockLo; s++ {
				starts = append(starts, s)
			}
		}
		if blockHi > cursor {
			cursor = blockHi
		}
	}
	for s := cursor; s+n <= l.Slots; s++ {
		starts = append(starts, s)
	}
	return starts
}

// CanAllocate 判断指定起始位置能否分配 n 个频隙。
func (l *Link) CanAllocate(start, n, guard int) bool {
	if start < 0 || start+n > l.Slots {
		return false
	}
	for _, iv := range l.data {
		if start < iv[1]+guard && iv[0]-guard < start+n {
			return false
		}
	}
	return true
}

// Allocate 在链路上登记中继段 segID 的数据频隙区间。
func (l *Link) Allocate(segID, start, n, guard int) bool {
	if !l.CanAllocate(start, n, guard) {
		return false
	}
	l.data[segID] = [2]int{start, start + n}
	return true
}

// Release 释放中继段在本链路上的频隙。
func (l *Link) Release(segID int) {
	delete(l.data, segID)
}

// SegmentStart 返回中继段在本链路上的起始频隙，不存在时返回 false。
func (l *Link) SegmentStart(segID int) (int, bool) {
	iv, ok := l.data[segID]
	if !ok {
		return 0, false
	}
	return iv[0], true
}

// FreeBlocks 返回所有连续空闲块（忽略保护带，按物理空闲频隙计），
// 碎片指标基于这些块计算。
func (l *Link) FreeBlocks() []Interval {
	used := l.Intervals()
	var blocks []Interval
	cursor := 0
	for _, iv := range used {
		if iv.Start > cursor {
			blocks = append(blocks, Interval{Start: cursor, End: iv.Start})
		}
		if iv.End > cursor {
			cursor = iv.End
		}
	}
	if cursor < l.Slots {
		blocks = append(blocks, Interval{Start: cursor, End: l.Slots})
	}
	return blocks
}

// LargestFreeBlock 返回最大连续空闲块的频隙数。
func (l *Link) LargestFreeBlock() int {
	best := 0
	for _, b := range l.FreeBlocks() {
		if n := b.End - b.Start; n > best {
			best = n
		}
	}
	return best
}

// Segment 一条连接在两个电中继站点之间的透明传输段。
// 同一个中继段在沿途每条链路上的频隙位置必须完全一致。
type Segment struct {
	ID     int
	Path   []int // 节点序列
	Links  []int // 链路 ID 序列
	Length float64
	Mod    Modulation
	Start  int // 起始频隙（全程一致）
	Slots  int // 数据频隙数
}

// Connection 一条端到端业务通道，可由多个中继段拼接而成。
type Connection struct {
	ID        string
	Src, Dst  int
	RateGbps  int
	Segments  []*Segment
	Arrive    float64 // 到达时间（仿真月）
	Leave     float64 // 拆除时间（仿真月）
	Blocked   bool
	FailLinks []int // 阻塞时记录的瓶颈链路候选
}

// RegenCount 电中继数量 = 中继段数 - 1。
func (c *Connection) RegenCount() int {
	if len(c.Segments) == 0 {
		return 0
	}
	return len(c.Segments) - 1
}

// TotalSlots 各中继段频隙数之和（不同段可换频谱位置）。
func (c *Connection) TotalSlots() int {
	n := 0
	for _, s := range c.Segments {
		n += s.Slots
	}
	return n
}

// Network 干线传输网：节点、链路、邻接表和在线连接。
type Network struct {
	Nodes      []*Node
	Links      []*Link
	adj        map[int][]int // 节点 ID -> 相邻链路 ID
	conns      map[string]*Connection
	segOwners  map[int]string // 中继段 ID -> 连接 ID
	nextSegID  int
	GuardSlots int
}

// NewNetwork 创建空网络。
func NewNetwork(guardSlots int) *Network {
	return &Network{
		adj:        make(map[int][]int),
		conns:      make(map[string]*Connection),
		segOwners:  make(map[int]string),
		GuardSlots: guardSlots,
	}
}

// AddNode 增加节点。
func (n *Network) AddNode(node *Node) {
	for node.ID >= len(n.Nodes) {
		n.Nodes = append(n.Nodes, nil)
	}
	n.Nodes[node.ID] = node
}

// AddLink 增加一条双向链路并建立邻接关系。
func (n *Network) AddLink(l *Link) {
	l.ID = len(n.Links)
	n.Links = append(n.Links, l)
	n.adj[l.U] = append(n.adj[l.U], l.ID)
	n.adj[l.V] = append(n.adj[l.V], l.ID)
}

// Link 按 ID 取链路。
func (n *Network) GetLink(id int) *Link { return n.Links[id] }

// Conn 按 ID 取连接。
func (n *Network) GetConn(id string) (*Connection, bool) {
	c, ok := n.conns[id]
	return c, ok
}

// Conns 返回全部在线连接。
func (n *Network) Conns() map[string]*Connection { return n.conns }

// ConnOfSegment 返回中继段所属连接 ID。
func (n *Network) ConnOfSegment(segID int) (string, bool) {
	id, ok := n.segOwners[segID]
	return id, ok
}

// AdjLinks 返回节点相邻的链路 ID。
func (n *Network) AdjLinks(nodeID int) []int { return n.adj[nodeID] }

// PathLinks 把节点路径翻译成链路 ID 序列，路径不相邻时返回 false。
func (n *Network) PathLinks(path []int) ([]int, bool) {
	links := make([]int, 0, len(path)-1)
	for i := 0; i+1 < len(path); i++ {
		found := -1
		for _, lid := range n.adj[path[i]] {
			l := n.Links[lid]
			if l.OtherEnd(path[i]) == path[i+1] {
				found = lid
				break
			}
		}
		if found < 0 {
			return nil, false
		}
		links = append(links, found)
	}
	return links, true
}

// PathLength 计算路径总长（km）。
func (n *Network) PathLength(linkIDs []int) float64 {
	var total float64
	for _, lid := range linkIDs {
		total += n.Links[lid].LengthKm
	}
	return total
}

// AllocateConnection 把一条已确定中继段频隙位置的连接写入网络。
// 任一段失败则整体回滚，保证网络状态一致。
func (n *Network) AllocateConnection(c *Connection) bool {
	type alloc struct{ lid, segID, start, slots int }
	done := make([]alloc, 0)
	rollback := func() {
		for _, a := range done {
			n.Links[a.lid].Release(a.segID)
		}
	}
	for _, seg := range c.Segments {
		if seg.ID == 0 {
			n.nextSegID++
			seg.ID = n.nextSegID
		}
		for _, lid := range seg.Links {
			if !n.Links[lid].Allocate(seg.ID, seg.Start, seg.Slots, n.GuardSlots) {
				rollback()
				return false
			}
			done = append(done, alloc{lid, seg.ID, seg.Start, seg.Slots})
		}
		n.segOwners[seg.ID] = c.ID
	}
	n.conns[c.ID] = c
	return true
}

// ReleaseConnection 拆除连接并释放所有中继段频隙。
func (n *Network) ReleaseConnection(id string) bool {
	c, ok := n.conns[id]
	if !ok {
		return false
	}
	for _, seg := range c.Segments {
		for _, lid := range seg.Links {
			n.Links[lid].Release(seg.ID)
		}
		delete(n.segOwners, seg.ID)
	}
	delete(n.conns, id)
	return true
}

// Clone 深拷贝网络（连接与链路占用全部复制），用于扩容收益的
// 反事实试算：在副本上做改动不影响真实仿真状态。
func (n *Network) Clone() *Network {
	cp := NewNetwork(n.GuardSlots)
	cp.Nodes = make([]*Node, len(n.Nodes))
	for i, node := range n.Nodes {
		nodeCopy := *node
		cp.Nodes[i] = &nodeCopy
	}
	cp.Links = make([]*Link, len(n.Links))
	for i, l := range n.Links {
		cp.Links[i] = &Link{
			ID: l.ID, U: l.U, V: l.V, LengthKm: l.LengthKm, Slots: l.Slots,
			data: make(map[int][2]int, len(l.data)),
		}
		for k, v := range l.data {
			cp.Links[i].data[k] = v
		}
	}
	cp.adj = make(map[int][]int, len(n.adj))
	for k, v := range n.adj {
		cp.adj[k] = append([]int(nil), v...)
	}
	cp.nextSegID = n.nextSegID
	cp.segOwners = make(map[int]string, len(n.segOwners))
	for k, v := range n.segOwners {
		cp.segOwners[k] = v
	}
	cp.conns = make(map[string]*Connection, len(n.conns))
	for id, c := range n.conns {
		cc := *c
		cc.Segments = make([]*Segment, len(c.Segments))
		for i, s := range c.Segments {
			ss := *s
			ss.Path = append([]int(nil), s.Path...)
			ss.Links = append([]int(nil), s.Links...)
			cp.segOwners[s.ID] = id
			cc.Segments[i] = &ss
		}
		cp.conns[id] = &cc
	}
	return cp
}
