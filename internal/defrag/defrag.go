// Package defrag 实现频谱碎片整理（defragmentation）。
//
// 约束：
//   - 业务不能断：采用推挽式（push-pull）无损重调，中继段整体在
//     沿途链路上同步移动，不释放再重建；
//   - 只能沿频隙编号减小的方向（向左）逐 slot 推，不能右移；
//   - 不能跳过其他通道：目标位置被占用或保护带不足时只能停住；
//   - 每个中继段保持其各链路上位置一致。
package defrag

import (
	"eonsa/internal/model"
)

// Move 一次中继段挪动：From -> To（To < From，逐 slot 推动）。
type Move struct {
	Seq       int     `json:"seq"`
	SegID     int     `json:"segId"`
	ConnID    string  `json:"connId"`
	LinkIDs   []int   `json:"linkIds"`
	From      int     `json:"from"`
	To        int     `json:"to"`
	Slots     int     `json:"slots"`
	Mod       string  `json:"mod"`
	DurationS float64 `json:"durationS"` // 本次挪动预计耗时（秒）
}

// Plan 一次碎片整理的完整挪动计划。
type Plan struct {
	Moves         []Move  `json:"moves"`
	TotalDuration float64 `json:"totalDurationS"`
	BeforeEF      float64 `json:"beforeEF"`
	AfterEF       float64 `json:"afterEF"`
}

// Config 整理参数。
type Config struct {
	// RetuneTimeS 每个频隙的推挽重调耗时（秒）。工程上推挽重调
	// 速率约每秒数个频隙，默认每 slot 2 秒。
	RetuneTimeS float64
	// SetupTimeS 每次挪动的固定准备开销（控制信令、确认）。
	SetupTimeS float64
	// MaxMoves 挪动次数上限，防止极端情况下计划过长。
	MaxMoves int
	// DisableGuard 关闭“全局碎片率不恶化”守卫，强制全部左压缩。
	// 默认 false：只执行净收益为正的挪动。
	DisableGuard bool
}

// DefaultConfig 默认整理参数。
func DefaultConfig() Config {
	return Config{RetuneTimeS: 2.0, SetupTimeS: 3.0, MaxMoves: 5000}
}

// Defragment 对全网执行向左压缩整理，返回挪动计划并把计划应用到
// 网络上。算法按轮次扫描所有中继段，每轮把能左移的段一次性移动到
// 最左可行位置；被挡住的段等待挡住它的段在后续轮次移走。
//
// 无死锁性：每个被执行的 Move 都严格减小某段的起始频隙，起始频隙
// 有下界 0 且段总数有限，因此轮次必然终止；且段只向左移动，相对
// 顺序不会反转，不会出现互相等待的环。
func Defragment(net *model.Network, cfg Config) *Plan {
	if cfg.RetuneTimeS <= 0 {
		cfg = DefaultConfig()
	}
	plan := &Plan{}
	plan.BeforeEF = meanEF(net)

	// 收集全网所有中继段，按 (最小链路ID, 当前起始位置) 稳定排序，
	// 让同一区域的段按当前布局顺序处理，挪动序列便于页面动画播放。
	type segRef struct {
		seg    *model.Segment
		connID string
	}
	refs := collectSegments(net)

	moveCount := 0
	for {
		progress := false
		currentEF := meanEF(net)
		for _, ref := range refs {
			if moveCount >= cfg.MaxMoves {
				break
			}
			seg := ref.seg
			target := leftmostFeasible(net, seg)
			if target >= seg.Start {
				continue // 已在最左可行位置（含无处可移）
			}
			// 全局守卫：试移动并比较全网平均碎片率，恶化则撤销跳过。
			// 轻载链路上的左压缩可能把一个大空闲块切成两块，此时
			// 不动反而更优——整理只做净收益为正的挪动。
			oldStart := seg.Start
			applyMove(net, seg, target)
			if !cfg.DisableGuard {
				if newEF := meanEF(net); newEF > currentEF+1e-12 {
					applyMove(net, seg, oldStart)
					continue
				} else {
					currentEF = newEF
				}
			} else {
				currentEF = meanEF(net)
			}
			distance := oldStart - target
			mv := Move{
				Seq:       len(plan.Moves) + 1,
				SegID:     seg.ID,
				ConnID:    ref.connID,
				LinkIDs:   append([]int(nil), seg.Links...),
				From:      oldStart,
				To:        target,
				Slots:     seg.Slots,
				Mod:       seg.Mod.Name,
				DurationS: cfg.SetupTimeS + float64(distance)*cfg.RetuneTimeS,
			}
			plan.Moves = append(plan.Moves, mv)
			plan.TotalDuration += mv.DurationS
			moveCount++
			progress = true
		}
		if !progress || moveCount >= cfg.MaxMoves {
			break
		}
	}
	plan.AfterEF = meanEF(net)
	return plan
}

// leftmostFeasible 求中继段在不跳过其他连接的前提下能推到的最左
// 起始位置。注意各链路的可行起点集合都可能不连续，不能对每链路
// 各自最左值取 max（该值未必在其他链路上可行）；必须先求所有链路
// 公共可行起点的交集，再取交集中的最小值。
func leftmostFeasible(net *model.Network, seg *model.Segment) int {
	// 摘掉本段在所有链路上的占用
	for _, lid := range seg.Links {
		net.GetLink(lid).Release(seg.ID)
	}
	common := map[int]bool{}
	for _, s := range net.GetLink(seg.Links[0]).FeasibleStarts(seg.Slots, net.GuardSlots) {
		common[s] = true
	}
	for _, lid := range seg.Links[1:] {
		next := map[int]bool{}
		for _, s := range net.GetLink(lid).FeasibleStarts(seg.Slots, net.GuardSlots) {
			if common[s] {
				next[s] = true
			}
		}
		common = next
		if len(common) == 0 {
			break
		}
	}
	// 恢复原占用
	for _, lid := range seg.Links {
		if !net.GetLink(lid).Allocate(seg.ID, seg.Start, seg.Slots, net.GuardSlots) {
			panic("defrag: failed to restore segment interval")
		}
	}
	if len(common) == 0 {
		return seg.Start // 无公共可行位置，本段不能动
	}
	best := seg.Start
	for s := range common {
		if s < best {
			best = s
		}
	}
	return best
}

// applyMove 执行一次无损挪动：先释放旧位置再登记新位置。
// 可行性已由 leftmostFeasible 校验，这里做防御性检查。
func applyMove(net *model.Network, seg *model.Segment, target int) {
	for _, lid := range seg.Links {
		l := net.GetLink(lid)
		l.Release(seg.ID)
	}
	for _, lid := range seg.Links {
		l := net.GetLink(lid)
		if !l.Allocate(seg.ID, target, seg.Slots, net.GuardSlots) {
			// 回滚到旧位置，保证状态一致
			for _, lid2 := range seg.Links {
				net.GetLink(lid2).Allocate(seg.ID, seg.Start, seg.Slots, net.GuardSlots)
			}
			panic("defrag: infeasible move detected")
		}
	}
	seg.Start = target
}

// collectSegments 收集全网中继段并稳定排序。
func collectSegments(net *model.Network) []struct {
	seg    *model.Segment
	connID string
} {
	type segRef = struct {
		seg    *model.Segment
		connID string
	}
	var refs []segRef
	for id, c := range net.Conns() {
		for _, seg := range c.Segments {
			refs = append(refs, segRef{seg: seg, connID: id})
		}
	}
	// 排序键：第一段链路 ID、起始位置、段 ID
	less := func(a, b segRef) bool {
		la, lb := a.seg.Links[0], b.seg.Links[0]
		if la != lb {
			return la < lb
		}
		if a.seg.Start != b.seg.Start {
			return a.seg.Start < b.seg.Start
		}
		return a.seg.ID < b.seg.ID
	}
	// 插入排序：段数量为连接数的若干倍，规模可控
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && less(refs[j], refs[j-1]); j-- {
			refs[j-1], refs[j] = refs[j], refs[j-1]
		}
	}
	return refs
}

// meanEF 全网按空闲量加权的平均外部碎片率（与 rsa 包指标一致，
// 此处独立实现以避免包循环）。
func meanEF(net *model.Network) float64 {
	var sumFree, sumEF float64
	for _, l := range net.Links {
		free := l.FreeSlots()
		sumFree += float64(free)
		if free > 0 {
			sumEF += (1 - float64(l.LargestFreeBlock())/float64(free)) * float64(free)
		} else {
			sumEF += float64(l.Slots) // 满链路碎片率记 1
		}
	}
	if sumFree == 0 {
		return 1
	}
	return sumEF / sumFree
}
