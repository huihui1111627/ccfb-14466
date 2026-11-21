// Package routing 实现基于光纤长度的 k 最短路径计算（Yen 算法），
// 为 RSA 提供候选路径集合。
package routing

import (
	"container/heap"
	"math"

	"eonsa/internal/model"
)

type dijkstraItem struct {
	node int
	dist float64
}

type pq []dijkstraItem

func (p pq) Len() int            { return len(p) }
func (p pq) Less(i, j int) bool  { return p[i].dist < p[j].dist }
func (p pq) Swap(i, j int)       { p[i], p[j] = p[j], p[i] }
func (p *pq) Push(x interface{}) { *p = append(*p, x.(dijkstraItem)) }
func (p *pq) Pop() interface{} {
	old := *p
	n := len(old)
	it := old[n-1]
	*p = old[:n-1]
	return it
}

// dijkstra 在屏蔽部分链路和节点的图上求 src 到 dst 的最短节点路径。
// blockedLinks 的 key 为链路 ID，blockedNodes 的 key 为节点 ID。
func dijkstra(net *model.Network, src, dst int, blockedLinks map[int]bool, blockedNodes map[int]bool) ([]int, bool) {
	if blockedNodes[src] || blockedNodes[dst] {
		return nil, false
	}
	dist := make(map[int]float64)
	prev := make(map[int]int)
	dist[src] = 0
	h := &pq{{node: src, dist: 0}}
	heap.Init(h)
	for h.Len() > 0 {
		cur := heap.Pop(h).(dijkstraItem)
		if cur.dist > dist[cur.node] {
			continue
		}
		if cur.node == dst {
			break
		}
		for _, lid := range net.AdjLinks(cur.node) {
			if blockedLinks[lid] {
				continue
			}
			l := net.GetLink(lid)
			nb := l.OtherEnd(cur.node)
			if blockedNodes[nb] {
				continue
			}
			nd := cur.dist + l.LengthKm
			if old, ok := dist[nb]; !ok || nd < old {
				dist[nb] = nd
				prev[nb] = cur.node
				heap.Push(h, dijkstraItem{node: nb, dist: nd})
			}
		}
	}
	if _, ok := dist[dst]; !ok {
		return nil, false
	}
	// 回溯路径
	path := []int{dst}
	for path[len(path)-1] != src {
		p, ok := prev[path[len(path)-1]]
		if !ok {
			return nil, false
		}
		path = append(path, p)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, true
}

// pathKey 生成路径的去重键。
func pathKey(path []int) string {
	b := make([]byte, 0, len(path)*4)
	for _, n := range path {
		b = append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return string(b)
}

// KShortestPaths 用 Yen 算法返回 src 到 dst 最多 k 条节点简单路径，
// 按长度升序排列。候选路径在后续 RSA 阶段分别评估中继与频谱可行性。
func KShortestPaths(net *model.Network, src, dst, k int) [][]int {
	if k <= 0 || src == dst {
		return nil
	}
	first, ok := dijkstra(net, src, dst, nil, nil)
	if !ok {
		return nil
	}
	paths := [][]int{first}
	seen := map[string]bool{pathKey(first): true}

	for len(paths) < k {
		prev := paths[len(paths)-1]
		candidates := map[string][]int{}
		for i := 0; i < len(prev)-1; i++ {
			spurNode := prev[i]
			root := append([]int(nil), prev[:i+1]...)

			blockedLinks := map[int]bool{}
			for _, p := range paths {
				if len(p) <= i+1 {
					continue
				}
				same := true
				for j := 0; j <= i; j++ {
					if p[j] != root[j] {
						same = false
						break
					}
				}
				if !same {
					continue
				}
				// 屏蔽与根路径相同的其他最短路径在 spur 处使用的链路
				for _, lid := range net.AdjLinks(spurNode) {
					l := net.GetLink(lid)
					if l.OtherEnd(spurNode) == p[i+1] {
						blockedLinks[lid] = true
					}
				}
			}
			// 屏蔽根路径上的中间节点，防止成环
			blockedNodes := map[int]bool{}
			for _, n := range root[:len(root)-1] {
				blockedNodes[n] = true
			}
			spur, okD := dijkstra(net, spurNode, dst, blockedLinks, blockedNodes)
			if !okD {
				continue
			}
			total := append([]int(nil), root[:len(root)-1]...)
			total = append(total, spur...)
			key := pathKey(total)
			if !seen[key] {
				candidates[key] = total
			}
		}
		if len(candidates) == 0 {
			break
		}
		// 选候选中最短的一条加入结果
		var best []int
		bestLen := math.Inf(1)
		for key, p := range candidates {
			links, ok := net.PathLinks(p)
			if !ok {
				continue
			}
			length := net.PathLength(links)
			if length < bestLen {
				bestLen = length
				best = p
				_ = key
			}
			seen[key] = true
		}
		if best == nil {
			break
		}
		paths = append(paths, best)
	}
	return paths
}
