// Package rsa 实现弹性光网络的路由与频谱分配（RSA）：
// 候选路径上的中继分段、频谱连续性与一致性约束下的频隙放置，
// 以及阻塞时的瓶颈链路分析与扩容收益评估。
package rsa

import (
	"sort"

	"eonsa/internal/model"
	"eonsa/internal/routing"
)

// PlacementPolicy 频隙位置选择策略。
type PlacementPolicy int

const (
	// FirstFit 首次适配：选编号最小的可用连续频隙段。
	FirstFit PlacementPolicy = iota
	// LastFit 最后适配：选编号最大的可用连续频隙段。
	LastFit
	// FragAware 碎片影响最小：选择放置后链路外部碎片率增量最小的位置。
	FragAware
)

// String 策略名称。
func (p PlacementPolicy) String() string {
	switch p {
	case FirstFit:
		return "first-fit"
	case LastFit:
		return "last-fit"
	case FragAware:
		return "frag-aware"
	}
	return "unknown"
}

// Request 业务申请。
type Request struct {
	ID       string
	Src, Dst int
	RateGbps int
	Arrive   float64
	Hold     float64
}

// Result 一次 RSA 求解结果。
type Result struct {
	Req       Request
	Success   bool
	Conn      *model.Connection
	Plan      *SegPlan
	PathCount int
	FailLinks []int // 阻塞时记录的瓶颈链路（按命中次数排序）
	Reason    string
}

// Solver RSA 求解器，配置在多条路径/多次请求间复用。
type Solver struct {
	K         int // 候选路径条数
	Segmenter *Segmenter
	Policy    PlacementPolicy
}

// NewSolver 默认求解器：5 条候选路径、默认分段器、首次适配。
func NewSolver(policy PlacementPolicy) *Solver {
	return &Solver{K: 5, Segmenter: NewSegmenter(), Policy: policy}
}

// candidate 一条候选路径的完整可行方案。
type candidate struct {
	plan *SegPlan
	conn *model.Connection
}

// Solve 在给定网络上为请求求解路由与频谱分配。
func (s *Solver) Solve(net *model.Network, req Request) *Result {
	res := &Result{Req: req}
	paths := routing.KShortestPaths(net, req.Src, req.Dst, s.K)
	res.PathCount = len(paths)
	if len(paths) == 0 {
		res.Reason = "拓扑上不存在可达路径"
		return res
	}

	hits := map[int]int{} // 链路在失败尝试中的命中次数
	var feasible []candidate

	for _, nodePath := range paths {
		plan := s.Segmenter.Plan(net, nodePath, req.RateGbps)
		if plan == nil {
			res.Reason = "路径过长且无法安排电中继"
			continue
		}
		conn := &model.Connection{
			ID: req.ID, Src: req.Src, Dst: req.Dst, RateGbps: req.RateGbps,
			Arrive: req.Arrive, Leave: req.Arrive + req.Hold,
		}
		ok := true
		for _, seg := range plan.Segments {
			starts := commonStarts(net, seg.Links, seg.Slots, net.GuardSlots)
			if len(starts) == 0 {
				ok = false
				recordTightLinks(net, seg, hits)
				break
			}
			seg.Start = s.chooseStart(net, seg, starts)
			conn.Segments = append(conn.Segments, seg)
		}
		if !ok {
			continue
		}
		feasible = append(feasible, candidate{plan: plan, conn: conn})
	}

	if len(feasible) == 0 {
		res.Reason = "候选路径均无满足连续性/一致性约束的频隙段"
		res.FailLinks = rankHits(hits)
		return res
	}

	// 路径间比较：中继最少优先，其次总频隙最少，最后按策略意图
	// 比较已选起始位置（首次适配偏向低 slot、最后适配偏向高 slot），
	// 保证不同放置策略在等价路径间会做出不同决策。
	sort.Slice(feasible, func(i, j int) bool {
		a, b := feasible[i], feasible[j]
		if a.plan.RegenCount != b.plan.RegenCount {
			return a.plan.RegenCount < b.plan.RegenCount
		}
		if a.plan.SlotTotal != b.plan.SlotTotal {
			return a.plan.SlotTotal < b.plan.SlotTotal
		}
		// 完全同中继/同频隙数时，按策略意图比较方案占用的最低
		// 频隙位置：LF 选位置最高的（保持低频隙区成整块），
		// FF/FragAware 选位置最低、最紧凑的。
		sa := minStart(a.conn)
		sb := minStart(b.conn)
		if sa != sb {
			if s.Policy == LastFit {
				return sa > sb
			}
			return sa < sb
		}
		// 完全同位置时按路径长度稳定排序
		la := net.PathLength(a.conn.Segments[0].Links)
		lb := net.PathLength(b.conn.Segments[0].Links)
		return la < lb
	})
	best := feasible[0]
	if !net.AllocateConnection(best.conn) {
		// 理论上不会发生（可行性已校验），保守处理
		res.Reason = "分配提交时发生冲突"
		res.FailLinks = rankHits(hits)
		return res
	}
	res.Success = true
	res.Conn = best.conn
	res.Plan = best.plan
	return res
}

// minStart 返回一条连接所有中继段中最低的起始频隙。
func minStart(c *model.Connection) int {
	m := c.Segments[0].Start
	for _, seg := range c.Segments[1:] {
		if seg.Start < m {
			m = seg.Start
		}
	}
	return m
}

// commonStarts 求一组链路上都能容纳 n 个频隙的公共起始位置
// （频谱全程位置一致约束的直接体现）。
func commonStarts(net *model.Network, linkIDs []int, n, guard int) []int {
	if len(linkIDs) == 0 {
		return nil
	}
	common := map[int]bool{}
	for _, s := range net.GetLink(linkIDs[0]).FeasibleStarts(n, guard) {
		common[s] = true
	}
	for _, lid := range linkIDs[1:] {
		next := map[int]bool{}
		for _, s := range net.GetLink(lid).FeasibleStarts(n, guard) {
			if common[s] {
				next[s] = true
			}
		}
		common = next
		if len(common) == 0 {
			return nil
		}
	}
	out := make([]int, 0, len(common))
	for s := range common {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

// recordTightLinks 记录一个放不下的中继段中最紧张的链路：
// 最大空闲块最小的链路（并列时取空闲总量最少者）。
func recordTightLinks(net *model.Network, seg *model.Segment, hits map[int]int) {
	var tightID int
	tightLargest := 1 << 30
	tightFree := 1 << 30
	for _, lid := range seg.Links {
		l := net.GetLink(lid)
		m := MeasureLink(l)
		if m.LargestFreeBlock < tightLargest ||
			(m.LargestFreeBlock == tightLargest && m.FreeSlots < tightFree) {
			tightLargest = m.LargestFreeBlock
			tightFree = m.FreeSlots
			tightID = lid
		}
	}
	hits[tightID]++
}

// rankHits 把命中次数转成按次数降序的链路 ID 列表。
func rankHits(hits map[int]int) []int {
	type pair struct{ id, n int }
	pairs := make([]pair, 0, len(hits))
	for id, n := range hits {
		pairs = append(pairs, pair{id, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].id < pairs[j].id
	})
	out := make([]int, len(pairs))
	for i, p := range pairs {
		out[i] = p.id
	}
	return out
}

// chooseStart 按配置策略在公共可行起始位置中选一个。
func (s *Solver) chooseStart(net *model.Network, seg *model.Segment, starts []int) int {
	switch s.Policy {
	case LastFit:
		return starts[len(starts)-1]
	case FragAware:
		best := starts[0]
		bestScore := s.placementScore(net, seg, best)
		for _, st := range starts[1:] {
			sc := s.placementScore(net, seg, st)
			if sc < bestScore {
				bestScore = sc
				best = st
			}
		}
		return best
	default: // FirstFit
		return starts[0]
	}
}

// placementScore 临时把中继段放到指定位置，计算其各链路外部碎片率
// 之和，随后立即回滚。得分越低，放置后产生的碎片越少。
func (s *Solver) placementScore(net *model.Network, seg *model.Segment, start int) float64 {
	const tempSegID = -1
	score := 0.0
	for _, lid := range seg.Links {
		l := net.GetLink(lid)
		if !l.Allocate(tempSegID, start, seg.Slots, net.GuardSlots) {
			return 1e9 // 意外冲突，视为最差
		}
		score += MeasureLink(l).ExternalFrag
		l.Release(tempSegID)
	}
	return score
}
