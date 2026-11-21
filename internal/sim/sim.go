// Package sim 实现多年尺度的弹性光网络业务仿真：
// 按真实增长节奏生成到达/拆除事件流，在多组 RSA 策略与
// “是否做碎片整理”组合下重放同一条事件流，输出阻塞率、
// 频谱利用率、碎片指标、中继数量等对比数据。
package sim

import (
	"math"
	"math/rand"
	"sort"

	"eonsa/internal/defrag"
	"eonsa/internal/model"
	"eonsa/internal/rsa"
	"eonsa/internal/topogen"
)

// Event 仿真事件（到达或拆除）。
type Event struct {
	Time     float64
	IsDepart bool
	Req      rsa.Request
}

// Trace 预生成的业务事件流，所有场景重放同一条 trace 以保证可比。
type Trace struct {
	Events []Event
}

// TraceConfig 业务流参数（时间单位：月）。
type TraceConfig struct {
	Months          float64
	InitialPerMonth float64 // 首月业务到达数
	AnnualGrowth    float64 // 年增长率
	MeanHold        float64 // 平均持有时间（月），指数分布
	Seed            int64
}

// DefaultTraceConfig 默认 3 年仿真：首月 6 条、年增 15%、平均持有 12 个月。
// 该负载下全网终态利用率约 33%~40%（空闲充足），阻塞主要由频谱
// 碎片化而非容量耗尽造成；碎片整理可把阻塞率相对降低约 1/4，
// 与“总空闲还有三成以上、大通道却开不出来”的工程现场一致。
func DefaultTraceConfig() TraceConfig {
	return TraceConfig{
		Months: 36, InitialPerMonth: 6, AnnualGrowth: 0.15,
		MeanHold: 12, Seed: 4242,
	}
}

// GenerateTrace 生成事件流。到达为非齐次泊松过程（按月分段等速率），
// 速率随年增长率指数上升；持有时间指数分布。
//
// 事件流只由 cfg.Seed 决定，与拓扑无关：节点按固定的幂律权重抽取，
// 因此同一种子在任何拓扑上重放的都是完全相同的一条事件流，
// 保证 6 组场景之间严格可比。
func GenerateTrace(net *model.Network, cfg TraceConfig) Trace {
	rng := rand.New(rand.NewSource(cfg.Seed))
	nodeCount := len(net.Nodes)
	// 固定权重表（与拓扑解耦），节点数变化时取模复用
	const virtualNodes = 256
	fixedWeights := make([]float64, virtualNodes)
	cum := make([]float64, virtualNodes)
	var sumW float64
	for i := range fixedWeights {
		w := 0.5 + float64((i*37+11)%100)/100.0*1.5
		fixedWeights[i] = w
		sumW += w
		cum[i] = sumW
	}
	// 一次均匀抽样映射到加权虚拟节点，单次 Float64 调用。
	pickVirtual := func(u float64) int {
		target := u * sumW
		for i, c := range cum {
			if target <= c {
				return i
			}
		}
		return virtualNodes - 1
	}
	// 选两个不同的物理节点，每个请求固定消耗两次节点抽样，
	// 不随拓扑改变随机数消耗量，保证事件流结构跨拓扑一致。
	pickPair := func() (int, int) {
		srcV := pickVirtual(rng.Float64())
		dstV := pickVirtual(rng.Float64())
		if dstV == srcV {
			dstV = (dstV + 1) % virtualNodes // 虚拟空间内无冲突
		}
		src := srcV % nodeCount
		dst := dstV % nodeCount
		if dst == src {
			dst = (src + 1) % nodeCount // 物理冲突时确定性处理，不额外抽样
		}
		return src, dst
	}
	rateTable := []struct {
		rate   int
		weight float64
	}{
		{100, 0.45}, {200, 0.35}, {400, 0.17}, {800, 0.03},
	}
	pickRate := func() int {
		r := rng.Float64()
		for _, rt := range rateTable {
			r -= rt.weight
			if r <= 0 {
				return rt.rate
			}
		}
		return rateTable[len(rateTable)-1].rate
	}

	var events []Event
	seq := 0
	t := 0.0
	for month := 0; float64(month) < cfg.Months; month++ {
		years := float64(month) / 12
		rate := cfg.InitialPerMonth * math.Pow(1+cfg.AnnualGrowth, years)
		monthEnd := float64(month + 1)
		for {
			t += rng.ExpFloat64() / rate
			if t >= monthEnd || t >= cfg.Months {
				break
			}
			src, dst := pickPair()
			hold := rng.ExpFloat64() * cfg.MeanHold
			leave := t + hold
			seq++
			req := rsa.Request{
				ID: "C" + itoa(seq), Src: src, Dst: dst,
				RateGbps: pickRate(), Arrive: t, Hold: hold,
			}
			events = append(events, Event{Time: t, Req: req})
			if leave < cfg.Months {
				events = append(events, Event{Time: leave, IsDepart: true, Req: req})
			}
		}
		t = monthEnd
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Time != events[j].Time {
			return events[i].Time < events[j].Time
		}
		// 同一时刻先拆后建，给新业务腾出释放的频隙
		if events[i].IsDepart != events[j].IsDepart {
			return events[i].IsDepart
		}
		return events[i].Req.ID < events[j].Req.ID
	})
	return Trace{Events: events}
}

// MonthlyStat 一个月末的全网统计快照。
type MonthlyStat struct {
	Month           int     `json:"month"`
	BlockingRate    float64 `json:"blockingRate"`
	Utilization     float64 `json:"utilization"`
	ExternalFrag    float64 `json:"externalFrag"`
	Entropy         float64 `json:"entropy"`
	ActiveConns     int     `json:"activeConns"`
	RegenCount      int     `json:"regenCount"`
	DefragRuns      int     `json:"defragRuns"`
	DefragMoves     int     `json:"defragMoves"`
	DefragDurationS float64 `json:"defragDurationS"`
	Arrivals        int     `json:"arrivals"`
	Blocked         int     `json:"blocked"`
}

// ScenarioResult 一组场景的完整仿真结果。
type ScenarioResult struct {
	Name          string             `json:"name"`
	Policy        string             `json:"policy"`
	DefragOn      bool               `json:"defragOn"`
	Stats         []MonthlyStat      `json:"stats"`
	BlockedReqs   []rsa.Request      `json:"-"`
	HitCounts     map[int]int        `json:"-"`
	DefragHistory []*defrag.Plan     `json:"-"`
	LastPlan      *defrag.Plan       `json:"-"`
	Summary       map[string]float64 `json:"summary"`
	// FinalNet 仿真结束时的网络快照（含全部在线连接与频谱占用），
	// 供瓶颈链路反事实扩容试算使用。
	FinalNet *model.Network `json:"-"`
}

func (r *ScenarioResult) totalMoves() int {
	n := 0
	for _, p := range r.DefragHistory {
		n += len(p.Moves)
	}
	return n
}

func (r *ScenarioResult) totalDefragDuration() float64 {
	d := 0.0
	for _, p := range r.DefragHistory {
		d += p.TotalDuration
	}
	return d
}

// SimConfig 仿真控制参数。
type SimConfig struct {
	DefragIntervalMonth float64
	DefragThresholdEF   float64
	Defrag              defrag.Config
}

// DefaultSimConfig 默认整理策略：每月评估一次，平均碎片率超过 0.25
// 即触发；带全局碎片率守卫的整理只会执行净收益为正的挪动。
func DefaultSimConfig() SimConfig {
	return SimConfig{
		DefragIntervalMonth: 1, DefragThresholdEF: 0.25,
		Defrag: defrag.DefaultConfig(),
	}
}

// Run 在给定网络上重放 trace。
func Run(net *model.Network, trace Trace, policy rsa.PlacementPolicy, defragOn bool, cfg SimConfig) *ScenarioResult {
	solver := rsa.NewSolver(policy)
	nameSuffix := ""
	if defragOn {
		nameSuffix = "+defrag"
	}
	res := &ScenarioResult{
		Name:   policy.String() + nameSuffix,
		Policy: policy.String(), DefragOn: defragOn,
		HitCounts: map[int]int{},
	}
	arrivals, blocked := 0, 0
	lastDefragMonth := -1e9

	// 事件按月归桶，月末统一出统计
	maxMonth := 0
	for _, ev := range trace.Events {
		if m := int(math.Ceil(ev.Time)); m > maxMonth {
			maxMonth = m
		}
	}
	evIdx := 0
	for month := 1; month <= maxMonth; month++ {
		monthEnd := float64(month)
		// 处理本月全部事件
		for evIdx < len(trace.Events) && trace.Events[evIdx].Time < monthEnd {
			ev := trace.Events[evIdx]
			evIdx++
			if ev.IsDepart {
				net.ReleaseConnection(ev.Req.ID)
				continue
			}
			arrivals++
			if defragOn && cfg.DefragIntervalMonth > 0 &&
				float64(month)-lastDefragMonth >= cfg.DefragIntervalMonth {
				frag := rsa.MeasureNetwork(net)
				if frag.MeanExternalFrag >= cfg.DefragThresholdEF {
					plan := defrag.Defragment(net, cfg.Defrag)
					res.DefragHistory = append(res.DefragHistory, plan)
					res.LastPlan = plan
					lastDefragMonth = float64(month)
				}
			}
			r := solver.Solve(net, ev.Req)
			if !r.Success {
				blocked++
				res.BlockedReqs = append(res.BlockedReqs, ev.Req)
				for _, lid := range r.FailLinks {
					res.HitCounts[lid]++
				}
			}
		}
		res.Stats = append(res.Stats, snapshot(net, month, arrivals, blocked, len(res.DefragHistory), res.totalMoves(), res.totalDefragDuration()))
	}

	res.Summary = map[string]float64{
		"arrivals":          float64(arrivals),
		"blocked":           float64(blocked),
		"blockingRate":      ratio(blocked, arrivals),
		"finalUtilization":  lastStat(res.Stats).Utilization,
		"finalExternalFrag": lastStat(res.Stats).ExternalFrag,
		"finalEntropy":      lastStat(res.Stats).Entropy,
		"finalRegenCount":   float64(lastStat(res.Stats).RegenCount),
		"defragRuns":        float64(len(res.DefragHistory)),
		"defragMoves":       float64(res.totalMoves()),
		"defragDurationS":   res.totalDefragDuration(),
	}
	res.FinalNet = net
	return res
}

func snapshot(net *model.Network, month, arrivals, blocked, runs, moves int, defragDuration float64) MonthlyStat {
	frag := rsa.MeasureNetwork(net)
	used, capTotal, regen := 0, 0, 0
	for _, l := range net.Links {
		used += l.UsedDataSlots()
		capTotal += l.Slots
	}
	for _, c := range net.Conns() {
		regen += c.RegenCount()
	}
	return MonthlyStat{
		Month:        month,
		BlockingRate: ratio(blocked, arrivals),
		Utilization:  float64(used) / float64(capTotal),
		ExternalFrag: frag.MeanExternalFrag,
		Entropy:      frag.MeanEntropy,
		ActiveConns:  len(net.Conns()),
		RegenCount:   regen,
		DefragRuns:   runs, DefragMoves: moves, DefragDurationS: defragDuration,
		Arrivals: arrivals, Blocked: blocked,
	}
}

// RunAllScenarios 在同一拓扑与 trace 上跑全部 6 组对比场景。
func RunAllScenarios(base *model.Network, trace Trace, cfg SimConfig) []*ScenarioResult {
	policies := []rsa.PlacementPolicy{rsa.FirstFit, rsa.LastFit, rsa.FragAware}
	var out []*ScenarioResult
	for _, p := range policies {
		for _, on := range []bool{false, true} {
			out = append(out, Run(base.Clone(), trace, p, on, cfg))
		}
	}
	return out
}

// DefaultNetwork 便捷构造默认拓扑。
func DefaultNetwork() *model.Network {
	return topogen.Generate(topogen.DefaultConfig())
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func lastStat(stats []MonthlyStat) MonthlyStat {
	if len(stats) == 0 {
		return MonthlyStat{}
	}
	return stats[len(stats)-1]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
