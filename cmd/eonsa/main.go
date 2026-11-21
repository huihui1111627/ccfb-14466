// Command eonsa 是弹性光网络 RSA 仿真系统的命令行入口，
// 支持拓扑生成、多年业务仿真对比、瓶颈链路分析与 Web 可视化。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"eonsa/internal/defrag"
	"eonsa/internal/rsa"
	"eonsa/internal/sim"
	"eonsa/internal/topogen"
	"eonsa/internal/webapi"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sim":
		cmdSim(os.Args[2:])
	case "bottleneck":
		cmdBottleneck(os.Args[2:])
	case "demo-defrag":
		cmdDemoDefrag(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	case "version":
		fmt.Println("eonsa 1.0.0 — 弹性光网络路由与频谱分配仿真系统")
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `eonsa — 弹性光网络 RSA 仿真系统

用法:
  eonsa sim [flags]            跑多年业务仿真，输出 6 组场景对比
  eonsa bottleneck [flags]     诊断阻塞瓶颈链路并评估扩容收益
  eonsa demo-defrag [flags]    构造碎片场景并导出整理挪动计划
  eonsa serve [flags]          启动 Web 可视化服务
  eonsa version                版本信息

通用 flags:
  -nodes N        拓扑节点数（默认 36）
  -slots N        每段光纤频隙数（默认 320）
  -months N       仿真时长（月，默认 36）
  -seed N         随机种子（默认 4242）
  -out DIR        结果输出目录（默认 ./out）
`)
}

func commonFlags(fs *flag.FlagSet) (*int, *int, *int, *int64, *string) {
	nodes := fs.Int("nodes", 36, "拓扑节点数")
	slots := fs.Int("slots", 320, "每段光纤频隙数")
	months := fs.Int("months", 36, "仿真时长（月）")
	seed := fs.Int64("seed", 4242, "随机种子")
	out := fs.String("out", "./out", "结果输出目录")
	return nodes, slots, months, seed, out
}

func cmdSim(args []string) {
	fs := flag.NewFlagSet("sim", flag.ExitOnError)
	nodes, slots, months, seed, out := commonFlags(fs)
	arrivals := fs.Float64("arrivals", 6, "首月业务到达数")
	growth := fs.Float64("growth", 0.15, "业务年增长率")
	hold := fs.Float64("hold", 12, "平均持有时间（月）")
	fs.Parse(args)

	topoCfg := topogen.DefaultConfig()
	topoCfg.NodeCount = *nodes
	topoCfg.Slots = *slots
	topoCfg.Seed = *seed
	base := topogen.Generate(topoCfg)

	tcfg := sim.DefaultTraceConfig()
	tcfg.Months = float64(*months)
	tcfg.InitialPerMonth = *arrivals
	tcfg.AnnualGrowth = *growth
	tcfg.MeanHold = *hold
	tcfg.Seed = *seed
	trace := sim.GenerateTrace(base, tcfg)

	results := sim.RunAllScenarios(base, trace, sim.DefaultSimConfig())
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	writeJSON(filepath.Join(*out, "scenarios.json"), results)
	writeJSON(filepath.Join(*out, "trace-summary.json"), map[string]any{
		"events": len(trace.Events),
		"months": *months,
		"nodes":  *nodes,
		"links":  len(base.Links),
		"slots":  *slots,
		"seed":   *seed,
	})

	printSimTable(results)
	fmt.Printf("\n结果已写入 %s/\n", *out)
}

func cmdBottleneck(args []string) {
	fs := flag.NewFlagSet("bottleneck", flag.ExitOnError)
	nodes, slots, months, seed, out := commonFlags(fs)
	extra := fs.Int("extra", 32, "试算扩容的频隙数")
	topN := fs.Int("top", 8, "输出候选链路数")
	fs.Parse(args)

	topoCfg := topogen.DefaultConfig()
	topoCfg.NodeCount = *nodes
	topoCfg.Slots = *slots
	topoCfg.Seed = *seed
	base := topogen.Generate(topoCfg)

	tcfg := sim.DefaultTraceConfig()
	tcfg.Months = float64(*months)
	tcfg.Seed = *seed
	trace := sim.GenerateTrace(base, tcfg)

	// 用首次适配、不整理场景制造阻塞样本；在仿真终态网络上做扩容试算
	res := sim.Run(base.Clone(), trace, rsa.FirstFit, false, sim.DefaultSimConfig())
	solver := rsa.NewSolver(rsa.FirstFit)
	report := rsa.AnalyzeBottlenecks(res.FinalNet, solver, res.BlockedReqs, res.HitCounts, *extra, *topN)

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	writeJSON(filepath.Join(*out, "bottleneck.json"), report)

	fmt.Println("阻塞瓶颈链路与扩容收益（反事实试算）:")
	fmt.Printf("%-12s %-10s %-12s %-16s %-12s\n", "链路", "命中次数", "碎片率",
		fmt.Sprintf("扩容+%d可挽回", *extra), "综合分")
	for _, c := range report.Candidates {
		l := base.GetLink(c.LinkID)
		fmt.Printf("%-8s %-10d %-12.3f %-14d %-12.1f\n",
			fmt.Sprintf("L%d(%d-%d)", c.LinkID, l.U, l.V),
			c.HitCount, c.MeanEF, c.Unblocked, c.BenefitScore)
	}
	fmt.Printf("\n结果已写入 %s/bottleneck.json\n", *out)
}

func cmdDemoDefrag(args []string) {
	fs := flag.NewFlagSet("demo-defrag", flag.ExitOnError)
	out := fs.String("out", "./out", "结果输出目录")
	fs.Parse(args)

	// 小规模演示网络：4 节点线性，每链路 40 频隙，手工制造碎片
	net := demoNetwork()
	before := rsa.MeasureNetwork(net)
	plan := defrag.Defragment(net, defrag.DefaultConfig())
	after := rsa.MeasureNetwork(net)

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	writeJSON(filepath.Join(*out, "defrag-plan.json"), plan)
	fmt.Printf("整理前平均碎片率 %.4f -> 整理后 %.4f\n", before.MeanExternalFrag, after.MeanExternalFrag)
	fmt.Printf("挪动 %d 次，预计总耗时 %.0f 秒（%.1f 分钟）\n",
		len(plan.Moves), plan.TotalDuration, plan.TotalDuration/60)
	for _, mv := range plan.Moves {
		fmt.Printf("  #%d 连接%s 段%d: slot %d -> %d（%d 个频隙，%.0fs）\n",
			mv.Seq, mv.ConnID, mv.SegID, mv.From, mv.To, mv.Slots, mv.DurationS)
	}
	fmt.Printf("\n挪动计划已写入 %s/defrag-plan.json\n", *out)
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	nodes := fs.Int("nodes", 36, "拓扑节点数")
	slots := fs.Int("slots", 320, "每段光纤频隙数")
	months := fs.Int("months", 36, "预跑仿真时长（月）")
	seed := fs.Int64("seed", 4242, "随机种子")
	addr := fs.String("addr", ":8080", "监听地址")
	fs.Parse(args)

	topoCfg := topogen.DefaultConfig()
	topoCfg.NodeCount = *nodes
	topoCfg.Slots = *slots
	topoCfg.Seed = *seed
	base := topogen.Generate(topoCfg)
	tcfg := sim.DefaultTraceConfig()
	tcfg.Months = float64(*months)
	tcfg.Seed = *seed
	trace := sim.GenerateTrace(base, tcfg)

	server := webapi.NewServer(base, trace, sim.DefaultSimConfig())
	fmt.Printf("EONSA Web 服务启动: http://localhost%s\n", *addr)
	if err := server.ListenAndServe(*addr); err != nil {
		fatal(err)
	}
}

// sim 包的结果类型在 webapi 与表格输出中直接使用。
func init() {}

func printSimTable(results []*sim.ScenarioResult) {
	fmt.Println("场景对比（同一拓扑、同一条业务事件流）:")
	fmt.Printf("%-22s %-10s %-10s %-12s %-12s %-10s %-12s %-10s\n",
		"场景", "请求数", "阻塞数", "阻塞率", "利用率", "中继数", "碎片率", "整理次数")
	for _, r := range results {
		s := r.Summary
		fmt.Printf("%-22s %-10.0f %-10.0f %-12.2f%% %-12.2f%% %-10.0f %-12.4f %-10.0f\n",
			r.Name, s["arrivals"], s["blocked"], s["blockingRate"]*100,
			s["finalUtilization"]*100, s["finalRegenCount"],
			s["finalExternalFrag"], s["defragRuns"])
	}
}

func writeJSON(path string, v any) {
	f, err := os.Create(path)
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}
