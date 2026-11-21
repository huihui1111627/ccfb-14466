// Package topogen 生成几十个节点规模的省干线传输网拓扑。
// 节点在平面上近似均匀铺开，链路基于 Bowyer–Watson 三角剖分
// 生成，并通过割点检测补齐边来保证双连通（任一站点失效不断网）。
// 同一种子生成完全相同的拓扑，保证多组仿真场景可对比。
package topogen

import (
	"math"
	"math/rand"
	"sort"

	"eonsa/internal/model"
)

// Config 拓扑生成参数。
type Config struct {
	NodeCount  int
	Slots      int
	Seed       int64
	GuardSlots int
	// DistanceScale 欧氏距离到光缆长度的放大系数（绕路、挂桥等）。
	DistanceScale float64
	// ExtraEdges 在三角剖分之外额外补的长距离干线数量。
	ExtraEdges int
}

// DefaultConfig 默认参数：36 节点、每链路 320 频隙。
func DefaultConfig() Config {
	return Config{
		NodeCount: 36, Slots: 320, GuardSlots: 1,
		DistanceScale: 1.15, ExtraEdges: 4, Seed: 20261122,
	}
}

type point struct{ x, y float64 }

// Generate 按配置生成网络。
func Generate(cfg Config) *model.Network {
	rng := rand.New(rand.NewSource(cfg.Seed))
	if cfg.NodeCount < 4 {
		cfg.NodeCount = 4
	}
	net := model.NewNetwork(cfg.GuardSlots)

	// 1) 近似均匀布点：网格 + 随机抖动，避免节点完全规则
	cols := int(math.Ceil(math.Sqrt(float64(cfg.NodeCount))))
	rows := int(math.Ceil(float64(cfg.NodeCount) / float64(cols)))
	dx, dy := 1000.0/float64(cols), 800.0/float64(rows)
	pts := make([]point, 0, cfg.NodeCount)
	for i := 0; i < cfg.NodeCount; i++ {
		cx := float64(i%cols) * dx
		cy := float64(i/cols) * dy
		jx := (rng.Float64() - 0.5) * dx * 0.6
		jy := (rng.Float64() - 0.5) * dy * 0.6
		pts = append(pts, point{cx + dx/2 + jx, cy + dy/2 + jy})
		net.AddNode(&model.Node{
			ID: i, Name: nodeName(i),
			X: pts[i].x, Y: pts[i].y,
		})
	}

	// 2) 三角剖分得到基础边集
	edgeSet := triangulate(pts)

	// 3) 补几条长距离干线，模拟跨区域直达光缆
	type cand struct {
		u, v int
		d    float64
	}
	var cands []cand
	for i := 0; i < cfg.NodeCount; i++ {
		for j := i + 1; j < cfg.NodeCount; j++ {
			if edgeSet[edgeKey(i, j)] {
				continue
			}
			cands = append(cands, cand{i, j, dist(pts[i], pts[j])})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].d > cands[j].d })
	// 只取较长且不会形成过度集中的边
	added := 0
	for _, c := range cands {
		if added >= cfg.ExtraEdges {
			break
		}
		edgeSet[edgeKey(c.u, c.v)] = true
		added++
	}

	// 4) 双连通保证：存在割点时补边消除
	ensureBiconnected(edgeSet, cfg.NodeCount, pts)

	// 5) 实例化链路
	keys := make([]string, 0, len(edgeSet))
	for k := range edgeSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		u, v := parseKey(k)
		length := dist(pts[u], pts[v]) * cfg.DistanceScale
		net.AddLink(model.NewLink(0, u, v, math.Round(length), cfg.Slots))
	}
	return net
}

// nodeName 站点命名：N01、N02……
func nodeName(i int) string {
	return "N" + pad2(i+1)
}

func pad2(i int) string {
	if i < 10 {
		return "0" + itoa(i)
	}
	return itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func dist(a, b point) float64 {
	return math.Hypot(a.x-b.x, a.y-b.y)
}

// ---- 边集工具 ----

func edgeKey(u, v int) string {
	if u > v {
		u, v = v, u
	}
	return itoa(u) + "-" + itoa(v)
}

func parseKey(k string) (int, int) {
	dash := -1
	for i, c := range k {
		if c == '-' {
			dash = i
			break
		}
	}
	return atoi(k[:dash]), atoi(k[dash+1:])
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// ---- Bowyer–Watson 三角剖分 ----

type tri struct{ a, b, c int }

func (t tri) contains(idx int) bool { return t.a == idx || t.b == idx || t.c == idx }

func triangulate(pts []point) map[string]bool {
	n := len(pts)
	// 超级三角形，足够大以包住所有点
	minX, minY, maxX, maxY := pts[0].x, pts[0].y, pts[0].x, pts[0].y
	for _, p := range pts[1:] {
		minX = math.Min(minX, p.x)
		maxX = math.Max(maxX, p.x)
		minY = math.Min(minY, p.y)
		maxY = math.Max(maxY, p.y)
	}
	dx, dy := maxX-minX, maxY-minY
	dmax := math.Max(dx, dy) * 20
	mx, my := (minX+maxX)/2, (minY+maxY)/2
	super := []point{
		{mx - dmax, my - dmax},
		{mx + dmax, my - dmax},
		{mx, my + dmax},
	}
	base := n
	all := append(append([]point{}, pts...), super...)
	tris := []tri{{base, base + 1, base + 2}}

	for i := 0; i < n; i++ {
		var bad []int
		for ti, t := range tris {
			if inCircumcircle(all[t.a], all[t.b], all[t.c], pts[i]) {
				bad = append(bad, ti)
			}
		}
		// 统计坏三角的边：只出现一次的边构成空腔边界
		edgeCount := map[string]int{}
		for _, ti := range bad {
			t := tris[ti]
			edgeCount[edgeKey(t.a, t.b)]++
			edgeCount[edgeKey(t.b, t.c)]++
			edgeCount[edgeKey(t.c, t.a)]++
		}
		// 移除坏三角
		badSet := map[int]bool{}
		for _, ti := range bad {
			badSet[ti] = true
		}
		kept := tris[:0]
		for ti, t := range tris {
			if !badSet[ti] {
				kept = append(kept, t)
			}
		}
		tris = kept
		for k, cnt := range edgeCount {
			if cnt != 1 {
				continue
			}
			u, v := parseKey(k)
			tris = append(tris, tri{i, u, v})
		}
	}

	edges := map[string]bool{}
	for _, t := range tris {
		if t.contains(base) || t.contains(base+1) || t.contains(base+2) {
			continue
		}
		edges[edgeKey(t.a, t.b)] = true
		edges[edgeKey(t.b, t.c)] = true
		edges[edgeKey(t.c, t.a)] = true
	}
	return edges
}

// inCircumcircle 判断点 p 是否在三角形 abc 的外接圆内。
func inCircumcircle(a, b, c, p point) bool {
	ax, ay := a.x-p.x, a.y-p.y
	bx, by := b.x-p.x, b.y-p.y
	cx, cy := c.x-p.x, c.y-p.y
	det := (ax*ax+ay*ay)*(bx*cy-cx*by) -
		(bx*bx+by*by)*(ax*cy-cx*ay) +
		(cx*cx+cy*cy)*(ax*by-bx*ay)
	return det > 0
}

// ---- 双连通补齐 ----

// ensureBiconnected 用 Tarjan 割点算法找割点。一个割点 v 在删除后
// 会产生 k 个连通分量，需要 k-1 条边把它们串起来；每轮处理一个
// 割点，循环到全网无割点为止。
func ensureBiconnected(edges map[string]bool, n int, pts []point) {
	for iter := 0; iter < 2*n; iter++ {
		adj := adjacency(edges, n)
		artic := findArticulation(adj, n)
		if len(artic) == 0 {
			return
		}
		v := artic[0]
		comps := componentsWithout(adj, n, v)
		if len(comps) < 2 {
			return
		}
		// 相邻分量之间取距离最近的节点对补边，把 k 个分量串成一条链
		for i := 0; i+1 < len(comps); i++ {
			bestU, bestV, bestD := comps[i][0], comps[i+1][0], math.Inf(1)
			for _, u := range comps[i] {
				for _, w := range comps[i+1] {
					if d := dist(pts[u], pts[w]); d < bestD {
						bestD, bestU, bestV = d, u, w
					}
				}
			}
			edges[edgeKey(bestU, bestV)] = true
		}
	}
}

// componentsWithout 返回删除节点 removed 后图的连通分量。
func componentsWithout(adj [][]int, n, removed int) [][]int {
	seen := make([]bool, n)
	seen[removed] = true
	var comps [][]int
	for start := 0; start < n; start++ {
		if seen[start] {
			continue
		}
		comp := []int{}
		stack := []int{start}
		seen[start] = true
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			comp = append(comp, cur)
			for _, nb := range adj[cur] {
				if !seen[nb] {
					seen[nb] = true
					stack = append(stack, nb)
				}
			}
		}
		comps = append(comps, comp)
	}
	return comps
}

func adjacency(edges map[string]bool, n int) [][]int {
	adj := make([][]int, n)
	for k := range edges {
		u, v := parseKey(k)
		adj[u] = append(adj[u], v)
		adj[v] = append(adj[v], u)
	}
	return adj
}

// findArticulation Tarjan 算法返回割点列表。
func findArticulation(adj [][]int, n int) []int {
	disc := make([]int, n)
	low := make([]int, n)
	parent := make([]int, n)
	for i := range disc {
		disc[i], parent[i] = -1, -1
	}
	timer := 0
	artic := map[int]bool{}
	var dfs func(u int)
	dfs = func(u int) {
		disc[u], low[u] = timer, timer
		timer++
		children := 0
		for _, v := range adj[u] {
			if disc[v] == -1 {
				children++
				parent[v] = u
				dfs(v)
				if low[v] < low[u] {
					low[u] = low[v]
				}
				if parent[u] == -1 && children > 1 {
					artic[u] = true
				}
				if parent[u] != -1 && low[v] >= disc[u] {
					artic[u] = true
				}
			} else if v != parent[u] && disc[v] < low[u] {
				low[u] = disc[v]
			}
		}
	}
	for u := 0; u < n; u++ {
		if disc[u] == -1 {
			dfs(u)
		}
	}
	out := make([]int, 0, len(artic))
	for v := range artic {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}
