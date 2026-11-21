package rsa

import (
	"sort"

	"eonsa/internal/model"
)

// ExpansionCandidate 一段候选扩容链路及其反事实收益。
type ExpansionCandidate struct {
	LinkID       int     `json:"linkId"`
	HitCount     int     `json:"hitCount"`
	MeanEF       float64 `json:"meanEF"`
	Unblocked    int     `json:"unblocked"`
	BenefitScore float64 `json:"benefitScore"`
}

// BottleneckReport 业务开不出来时给规划人员的诊断报告。
type BottleneckReport struct {
	Candidates []ExpansionCandidate `json:"candidates"`
}

// AnalyzeBottlenecks 基于阻塞请求的命中记录给出瓶颈链路，并用
// 反事实试算评估扩容收益：在网络副本上给候选链路增加 extraSlots
// 个频隙，重放 blocked 请求，统计能被挽回的请求数。
//
// solver 与构造 blocked 请求时使用的求解器保持一致；每次试算都在
// 独立副本上进行，不影响真实网络状态。
func AnalyzeBottlenecks(net *model.Network, solver *Solver, blocked []Request, hitCounts map[int]int, extraSlots int, topN int) *BottleneckReport {
	if extraSlots <= 0 {
		extraSlots = 32
	}
	if topN <= 0 {
		topN = 8
	}
	ids := rankHits(hitCounts)
	if len(ids) > topN {
		ids = ids[:topN]
	}
	out := make([]ExpansionCandidate, 0, len(ids))
	for _, lid := range ids {
		clone := net.Clone()
		clone.GetLink(lid).Slots += extraSlots

		unblocked := 0
		for _, req := range blocked {
			r := solver.Solve(clone, req)
			if r.Success {
				unblocked++
				clone.ReleaseConnection(r.Conn.ID) // 试算后立即拆除，保持副本基线一致
			}
		}
		m := MeasureLink(net.GetLink(lid))
		score := float64(unblocked)*100 + float64(hitCounts[lid]) + m.ExternalFrag*10
		out = append(out, ExpansionCandidate{
			LinkID:       lid,
			HitCount:     hitCounts[lid],
			MeanEF:       m.ExternalFrag,
			Unblocked:    unblocked,
			BenefitScore: score,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Unblocked != out[j].Unblocked {
			return out[i].Unblocked > out[j].Unblocked
		}
		return out[i].BenefitScore > out[j].BenefitScore
	})
	return &BottleneckReport{Candidates: out}
}
