// Package webapi 提供仿真系统的 HTTP 接口与静态页面服务：
// 拓扑与链路频谱快照、连接详情、6 组场景曲线、瓶颈诊断、
// 碎片整理挪动计划（含逐动作频谱快照，供前端播放动画）。
package webapi

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"eonsa/internal/defrag"
	"eonsa/internal/model"
	"eonsa/internal/rsa"
	"eonsa/internal/sim"
)

//go:embed static/*
var staticFS embed.FS

// Server 持有基线网络、预跑场景结果与一个可交互的演示网络副本。
type Server struct {
	mu        sync.Mutex
	base      *model.Network
	trace     sim.Trace
	simCfg    sim.SimConfig
	scenarios []*sim.ScenarioResult

	demo     *model.Network // 交互演示用可变副本
	seedDemo *model.Network // 预热后的初始演示状态，供重置使用
	demoSeq  int
}

// NewServer 预计算全部场景并初始化演示网络。
// 演示网络重放 trace 的前 warmupEvents 个到达事件（含拆除），
// 制造出接近“运行两年后”的真实碎片布局，供频谱展示与整理动画使用。
func NewServer(base *model.Network, trace sim.Trace, cfg sim.SimConfig) *Server {
	scenarios := sim.RunAllScenarios(base, trace, cfg)
	seeded := seedDemoNetwork(base, trace, 240)
	return &Server{
		base: base, trace: trace, simCfg: cfg,
		scenarios: scenarios, demo: seeded, seedDemo: seeded.Clone(),
	}
}

// seedDemoNetwork 在基线上重放前 n 个到达事件及其拆除事件，
// 得到一个带真实碎片的演示网络。
func seedDemoNetwork(base *model.Network, trace sim.Trace, n int) *model.Network {
	net := base.Clone()
	solver := rsa.NewSolver(rsa.FirstFit)
	arrived := 0
	for _, ev := range trace.Events {
		if ev.IsDepart {
			net.ReleaseConnection(ev.Req.ID)
			continue
		}
		if arrived >= n {
			break
		}
		arrived++
		if r := solver.Solve(net, ev.Req); r.Success {
			_ = r
		}
	}
	return net
}

// ListenAndServe 注册路由并启动 HTTP 服务。
func (s *Server) ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/topo", s.handleTopo)
	mux.HandleFunc("/api/spectrum", s.handleSpectrum)
	mux.HandleFunc("/api/connections", s.handleConnections)
	mux.HandleFunc("/api/scenarios", s.handleScenarios)
	mux.HandleFunc("/api/bottleneck", s.handleBottleneck)
	mux.HandleFunc("/api/defrag/plan", s.handleDefragPlan)
	mux.HandleFunc("/api/request", s.handleRequest)
	mux.HandleFunc("/api/reset", s.handleReset)
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	// 自定义静态处理：根路径直接返回 index.html，其他路径按嵌入 FS
	// 读取；不能把改写后的路径再交给 FileServer，否则会触发 301 循环。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" {
			p = "/index.html"
		}
		b, err := fs.ReadFile(sub, strings.TrimPrefix(p, "/"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch filepath.Ext(p) {
		case ".html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case ".js":
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		default:
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Write(b)
	})
	return http.ListenAndServe(addr, mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---- DTO ----

type nodeDTO struct {
	ID   int     `json:"id"`
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

type linkDTO struct {
	ID       int     `json:"id"`
	U        int     `json:"u"`
	V        int     `json:"v"`
	LengthKm float64 `json:"lengthKm"`
	Slots    int     `json:"slots"`
	Free     int     `json:"free"`
	Used     int     `json:"used"`
	Largest  int     `json:"largestFree"`
	EF       float64 `json:"externalFrag"`
}

type intervalDTO struct {
	SegID  int    `json:"segId"`
	ConnID string `json:"connId"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Mod    string `json:"mod"`
	Rate   int    `json:"rateGbps"`
	Color  string `json:"color"`
}

type segmentDTO struct {
	ID    int   `json:"id"`
	Links []int `json:"links"`
	Path  []int `json:"path"`
	Start int   `json:"start"`
	Slots int   `json:"slots"`
}

type connDTO struct {
	ID       string       `json:"id"`
	Src      int          `json:"src"`
	Dst      int          `json:"dst"`
	RateGbps int          `json:"rateGbps"`
	Regens   int          `json:"regens"`
	Segments []segmentDTO `json:"segments"`
}

type spectrumDTO struct {
	Link      linkDTO       `json:"link"`
	Intervals []intervalDTO `json:"intervals"`
	// Slots 数组：0 空闲，1 数据占用，2 保护带；前端直接渲染频谱条
	Grid []int `json:"grid"`
}

type topoDTO struct {
	Nodes []nodeDTO `json:"nodes"`
	Links []linkDTO `json:"links"`
	Guard int       `json:"guardSlots"`
}

func linkDTOFrom(l *model.Link) linkDTO {
	m := rsa.MeasureLink(l)
	return linkDTO{
		ID: l.ID, U: l.U, V: l.V, LengthKm: l.LengthKm, Slots: l.Slots,
		Free: l.FreeSlots(), Used: l.UsedDataSlots(),
		Largest: m.LargestFreeBlock, EF: m.ExternalFrag,
	}
}

// connColor 为连接分配稳定颜色（按 ID 哈希取色板）。
var palette = []string{
	"#4e79a7", "#f28e2b", "#59a14f", "#e15759", "#76b7b2",
	"#edc948", "#b07aa1", "#ff9da7", "#9c755f", "#bab0ac",
}

func connColor(id string) string {
	h := 0
	for _, c := range id {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return palette[h%len(palette)]
}

func buildSpectrum(net *model.Network, l *model.Link) spectrumDTO {
	dto := spectrumDTO{Link: linkDTOFrom(l), Grid: make([]int, l.Slots)}
	for _, iv := range l.Intervals() {
		connID, _ := net.ConnOfSegment(iv.SegID)
		mod, rate := "", 0
		if c, ok := net.GetConn(connID); ok {
			rate = c.RateGbps
			for _, seg := range c.Segments {
				if seg.ID == iv.SegID {
					mod = seg.Mod.Name
				}
			}
		}
		dto.Intervals = append(dto.Intervals, intervalDTO{
			SegID: iv.SegID, ConnID: connID,
			Start: iv.Start, End: iv.End, Mod: mod, Rate: rate,
			Color: connColor(connID),
		})
		for s := iv.Start; s < iv.End; s++ {
			dto.Grid[s] = 1
		}
		// 保护带标记（边界内）
		for g := 1; g <= net.GuardSlots; g++ {
			if iv.Start-g >= 0 && dto.Grid[iv.Start-g] == 0 {
				dto.Grid[iv.Start-g] = 2
			}
			if iv.End+g < l.Slots && dto.Grid[iv.End+g] == 0 {
				dto.Grid[iv.End+g] = 2
			}
		}
	}
	return dto
}

func (s *Server) handleTopo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := topoDTO{Guard: s.demo.GuardSlots}
	for _, n := range s.demo.Nodes {
		out.Nodes = append(out.Nodes, nodeDTO{ID: n.ID, Name: n.Name, X: n.X, Y: n.Y})
	}
	for _, l := range s.demo.Links {
		out.Links = append(out.Links, linkDTOFrom(l))
	}
	writeJSON(w, out)
}

func (s *Server) handleSpectrum(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spectrumDTO, 0, len(s.demo.Links))
	for _, l := range s.demo.Links {
		out = append(out, buildSpectrum(s.demo, l))
	}
	writeJSON(w, out)
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]connDTO, 0, len(s.demo.Conns()))
	for _, c := range s.demo.Conns() {
		dto := connDTO{
			ID: c.ID, Src: c.Src, Dst: c.Dst, RateGbps: c.RateGbps,
			Regens: c.RegenCount(),
		}
		for _, seg := range c.Segments {
			dto.Segments = append(dto.Segments, segmentDTO{
				ID: seg.ID, Links: seg.Links, Path: seg.Path,
				Start: seg.Start, Slots: seg.Slots,
			})
		}
		out = append(out, dto)
	}
	writeJSON(w, out)
}

func (s *Server) handleScenarios(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, s.scenarios)
}

func (s *Server) handleBottleneck(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 取首次适配不整理场景的阻塞样本
	var target *sim.ScenarioResult
	for _, sc := range s.scenarios {
		if sc.Policy == rsa.FirstFit.String() && !sc.DefragOn {
			target = sc
		}
	}
	if target == nil {
		http.Error(w, "scenario unavailable", http.StatusInternalServerError)
		return
	}
	solver := rsa.NewSolver(rsa.FirstFit)
	report := rsa.AnalyzeBottlenecks(target.FinalNet, solver, target.BlockedReqs, target.HitCounts, 32, 8)
	writeJSON(w, report)
}

// moveSnapshot 一次挪动后的全网频谱快照。
type moveSnapshot struct {
	Move     defrag.Move   `json:"move"`
	Spectrum []spectrumDTO `json:"spectrum"`
}

type defragPlanDTO struct {
	Plan      *defrag.Plan   `json:"plan"`
	Before    []spectrumDTO  `json:"before"`
	Snapshots []moveSnapshot `json:"snapshots"`
	After     []spectrumDTO  `json:"after"`
}

func (s *Server) handleDefragPlan(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 在副本上演示整理，主演示网络保持不变（前端可反复播放）
	clone := s.demo.Clone()
	before := make([]spectrumDTO, 0, len(clone.Links))
	for _, l := range clone.Links {
		before = append(before, buildSpectrum(clone, l))
	}
	plan := defrag.Defragment(clone, defrag.DefaultConfig())

	// 重新逐动作回放以生成中间快照
	play := s.demo.Clone()
	snaps := make([]moveSnapshot, 0, len(plan.Moves))
	segByID := map[int]*model.Segment{}
	for _, c := range play.Conns() {
		for _, seg := range c.Segments {
			segByID[seg.ID] = seg
		}
	}
	for _, mv := range plan.Moves {
		seg := segByID[mv.SegID]
		// 与 defrag 包相同的释放-重分配序列
		for _, lid := range seg.Links {
			play.GetLink(lid).Release(seg.ID)
		}
		for _, lid := range seg.Links {
			if !play.GetLink(lid).Allocate(seg.ID, mv.To, seg.Slots, play.GuardSlots) {
				http.Error(w, "snapshot replay failed", http.StatusInternalServerError)
				return
			}
		}
		seg.Start = mv.To
		snap := moveSnapshot{Move: mv}
		for _, l := range play.Links {
			snap.Spectrum = append(snap.Spectrum, buildSpectrum(play, l))
		}
		snaps = append(snaps, snap)
	}
	after := make([]spectrumDTO, 0, len(play.Links))
	for _, l := range play.Links {
		after = append(after, buildSpectrum(play, l))
	}
	writeJSON(w, defragPlanDTO{Plan: plan, Before: before, Snapshots: snaps, After: after})
}

type requestDTO struct {
	Src      int    `json:"src"`
	Dst      int    `json:"dst"`
	RateGbps int    `json:"rateGbps"`
	Policy   int    `json:"policy"` // 0 FF 1 LF 2 frag-aware
	Release  bool   `json:"release"`
	ConnID   string `json:"connId"`
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var dto requestDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if dto.Release {
		ok := s.demo.ReleaseConnection(dto.ConnID)
		writeJSON(w, map[string]any{"success": ok})
		return
	}
	s.demoSeq++
	policy := rsa.PlacementPolicy(dto.Policy)
	solver := rsa.NewSolver(policy)
	req := rsa.Request{
		ID: "WEB" + itoa(s.demoSeq), Src: dto.Src, Dst: dto.Dst,
		RateGbps: dto.RateGbps,
	}
	res := solver.Solve(s.demo, req)
	if !res.Success {
		writeJSON(w, map[string]any{
			"success": false, "reason": res.Reason, "failLinks": res.FailLinks,
		})
		return
	}
	writeJSON(w, map[string]any{"success": true, "connId": res.Conn.ID,
		"regens": res.Conn.RegenCount(), "segments": res.Conn.Segments})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demo = s.seedDemo.Clone()
	s.demoSeq = 0
	writeJSON(w, map[string]any{"success": true})
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
