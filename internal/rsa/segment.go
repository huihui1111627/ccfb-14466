package rsa

import (
	"math"

	"eonsa/internal/model"
)

// SegPlan 一条候选路径上的中继分段方案（尚未确定频隙位置）。
type SegPlan struct {
	Segments   []*model.Segment
	SlotTotal  int     // 各段频隙数之和
	RegenCount int     // 电中继数量
	Cost       float64 // 频谱 + 中继的加权代价
}

// Segmenter 负责在一条节点路径上决定电中继位置与各段调制方式。
type Segmenter struct {
	// RegenWeight 一个电中继折算成多少个频隙的代价权重。
	// 中继很贵：默认权重远高于单段频隙差异，只有距离迫使或能显著
	// 降低频谱占用时才会增加中继。
	RegenWeight float64
	MaxRegens   int
}

// NewSegmenter 默认分段器。
func NewSegmenter() *Segmenter {
	return &Segmenter{RegenWeight: 8, MaxRegens: 6}
}

// Plan 在给定节点路径上做动态规划，求中继数量与频谱占用之间
// 权衡最优的分段方案。
//
// dp[j] 表示到达路径上第 j 个节点的最小代价；一次转移 i->j 构成
// 一个透明传输段，要求段长不超过 BPSK 极限，段内按距离选最高阶
// 调制。转移 i>0 意味着在节点 i 放置一个电中继。
func (s *Segmenter) Plan(net *model.Network, nodePath []int, rateGbps int) *SegPlan {
	linkIDs, ok := net.PathLinks(nodePath)
	if !ok || len(linkIDs) == 0 {
		return nil
	}
	m := len(nodePath)

	// prefix[i]：路径起点到第 i 个节点的累计长度
	prefix := make([]float64, m)
	for i, lid := range linkIDs {
		prefix[i+1] = prefix[i] + net.GetLink(lid).LengthKm
	}
	bpsk := model.Modulations[len(model.Modulations)-1]

	type state struct {
		cost   float64
		parent int
		seg    *model.Segment
		regens int
	}
	inf := math.Inf(1)
	dp := make([]state, m)
	for i := 1; i < m; i++ {
		dp[i].cost = inf
		dp[i].parent = -1
	}
	dp[0].parent = -1

	for j := 1; j < m; j++ {
		for i := 0; i < j; i++ {
			if dp[i].cost == inf {
				continue
			}
			segLen := prefix[j] - prefix[i]
			if segLen > bpsk.MaxReachKm {
				continue // 单段 BPSK 也到不了，必须再拆
			}
			mod, ok := model.BestModulation(segLen)
			if !ok {
				continue
			}
			slots := model.SlotsNeeded(rateGbps, mod)
			cost := dp[i].cost + float64(slots)
			regen := 0
			if i > 0 {
				cost += s.RegenWeight
				regen = 1
			}
			if dp[i].regens+regen > s.MaxRegens {
				continue
			}
			if cost < dp[j].cost {
				seg := &model.Segment{
					Path:   append([]int(nil), nodePath[i:j+1]...),
					Links:  append([]int(nil), linkIDs[i:j]...),
					Length: segLen,
					Mod:    mod,
					Slots:  slots,
					Start:  -1,
				}
				dp[j] = state{
					cost: cost, parent: i, seg: seg,
					regens: dp[i].regens + regen,
				}
			}
		}
	}
	if dp[m-1].cost == inf {
		return nil
	}

	// 回溯分段
	var segs []*model.Segment
	for j := m - 1; j > 0; {
		st := dp[j]
		if st.parent < 0 {
			return nil
		}
		segs = append(segs, st.seg)
		j = st.parent
	}
	// 反转为沿路径方向
	for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
		segs[i], segs[j] = segs[j], segs[i]
	}
	total := 0
	for _, seg := range segs {
		total += seg.Slots
	}
	return &SegPlan{
		Segments:   segs,
		SlotTotal:  total,
		RegenCount: len(segs) - 1,
		Cost:       dp[m-1].cost,
	}
}
