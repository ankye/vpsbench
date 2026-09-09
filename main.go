package main

import (
	"fmt"
	"os"
)

const usageEn = `vpsbench — VPS performance / concurrency / oversell (shared-host steal) test tool
Single binary, two ways to use:
  1) Gin server + embedded web UI: run "server" on the VPS under test, open in browser, click to test
  2) CLI client: drive the same API from LAN / headless machines

Language: English by default; add "-lang zh" to any command, or set env VPSBENCH_LANG=zh

NO SERVER NEEDED: every client command works standalone — omit <url> and it tests
the local machine via an in-process server. Give <url> to test a remote box.

One-click acceptance inspection (new VPS, run on the box):
  vpsbench inspect [-y] [-cores 2 -ram 4 -disk 80]   8-step wizard, Enter through each step,
                                                     order-spec comparison + acceptance report

Server (only needed for browser UI or remote testing):
  vpsbench server [-addr :8300] [-dir /tmp] [-token XXX]   start test server (Gin + web UI)

Client (<url> optional = local self-test):
  vpsbench ping  [url] [-d 30s] [-i 100ms]     latency: RTT jitter & spikes (neighbor steal shows up here)
  vpsbench load  [url] [-c 50] [-d 30s] [-size 0]  HTTP load test: RPS / errors / percentiles (size=resp bytes)
  vpsbench packet [url] [-c 100] [-d 10s] [-sizes 1024,2048,4096]  packet-size ladder (1K/2K/4K)
  vpsbench conn  [url] [-c 500] [-hold 30s]    max concurrent connections (browser caps ~6, use CLI)
  vpsbench bw    [url] [-d 10s] [-c 4] [-up]   bandwidth (download / upload)
  vpsbench cpu   [url] [-ms 2000]              single-core benchmark (3 runs, best)
  vpsbench fill  [url] [-n 0=all] [-ms 5000]   fill n cores: oversell / cgroup-quota / throttling detection
  vpsbench disk  [url] [-mb 64] [-files 100]   disk: seq R/W bandwidth + small-file fsync latency
  vpsbench diag  [url] [-d 10]                 one-click diagnosis: which of CPU/GPU/IO/socket/mem/disk is
                                               the highest load, with p50/p95/p99 and ideal-value table
  vpsbench steal [url] [-w]                    steal / scheduling-jitter report (-w keeps watching)
  vpsbench all   [url]                         full suite + All-in-One server summary

Examples:
  standalone:  ./vpsbench inspect -lang zh -cores 2 -ram 4 -disk 80   # new VPS acceptance
  standalone:  ./vpsbench all                                          # local self-test, no server
  server:      ./vpsbench server -addr :8300                           # open firewall port
  client:      ./vpsbench all http://<vps-ip>:8300 -lang zh
  watch:       ./vpsbench steal http://<vps-ip>:8300 -w                # 24h to catch peak-hour steal

About "shared-host steal":
  - steal%:          CPU time the hypervisor admits giving to other tenants (/proc/stat)
  - scheduling jitter: actual-vs-expected delay of a 10ms sleep — the real latency of being stolen
  - high jitter/steal while idle => oversold host, noisy neighbors
`

const usageZh = `vpsbench — VPS 性能 / 并发 / 超售(共享宿主机偷取)检测工具
单二进制,两种用法:
  1) Gin 服务器 + 内嵌网页:server 跑在被测 VPS 上,浏览器打开直接点按钮测试
  2) CLI 客户端:内网/无浏览器环境命令行压测同一套 API

语言:默认英语防乱码;任意命令加 "-lang zh" 或设环境变量 VPSBENCH_LANG=zh 切中文

免 server:所有客户端命令可独立运行——省略 <url> 即进程内起服务自测本机;给 <url> 则测远端。

一键验货(新 VPS 到货,直接在机器上跑):
  vpsbench inspect [-y] [-cores 2 -ram 4 -disk 80]   8 步验收向导,一路回车,
                                                     支持订单配置比对 + 输出验收单

服务端(仅浏览器测试/远程测试需要):
  vpsbench server [-addr :8300] [-dir /tmp] [-token XXX]   启动测试服务(Gin + 网页)

客户端(<url> 可省略 = 本机自测):
  vpsbench ping  [url] [-d 30s] [-i 100ms]     延迟测试:RTT 抖动,检测邻居偷取导致的延迟尖刺
  vpsbench load  [url] [-c 50] [-d 30s] [-size 0]  HTTP 并发压测:RPS / 错误率 / 分位延迟(size=响应包字节)
  vpsbench packet [url] [-c 100] [-d 10s] [-sizes 1024,2048,4096]  包尺寸并发阶梯(1K/2K/4K)
  vpsbench conn  [url] [-c 500] [-hold 30s]    最大并发连接数(浏览器有 ~6 连接限制,请用 CLI)
  vpsbench bw    [url] [-d 10s] [-c 4] [-up]   上/下行带宽测试
  vpsbench cpu   [url] [-ms 2000]              单核算力测试(跑 3 轮取最优)
  vpsbench fill  [url] [-n 0全部核] [-ms 5000] 打满 n 核:检测核数超售 / cgroup 限速 / 降频
  vpsbench disk  [url] [-mb 64] [-files 100]   磁盘:顺序读写带宽 + 小文件 fsync 延迟(I/O 被偷取最敏感)
  vpsbench diag  [url] [-d 10]                 一键诊断:CPU/GPU/IO/socket/mem/磁盘 谁负载最高
                                               + p50/p95/p99 + 理想值对比表
  vpsbench steal [url] [-w]                    CPU steal / 调度抖动报告(-w 持续观察)
  vpsbench all   [url]                         全套测试 + All-in-One 服务器信息汇总

示例:
  本机验货:  ./vpsbench inspect -cores 2 -ram 4 -disk 80     # 新 VPS 一键验收
  本机自测:  ./vpsbench all                                    # 不用开 server
  服务端:    ./vpsbench server -addr :8300                     # 记得防火墙放行端口
  客户端:    ./vpsbench all http://<vps-ip>:8300 -lang zh
  蹲守偷取:  ./vpsbench steal http://<vps-ip>:8300 -w          # 挂 24h 抓高峰期邻居偷取

关于"共享主机被偷取":
  - steal%:   虚拟化层承认的被其他租户占走的 CPU 时间占比(/proc/stat)
  - 调度抖动: 进程 sleep 10ms 的实际偏差——被偷取造成的真实延迟体现
  - 空闲时段抖动大 / steal 高 ⇒ 宿主机超售,邻居在抢资源
`

func usage() string {
	if langName == "zh" {
		return usageZh
	}
	return usageEn
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage())
		os.Exit(2)
	}
	cmd, args := os.Args[1], InitLang(os.Args[2:])
	switch cmd {
	case "server":
		runServer(args)
	case "ping":
		runPingCmd(args)
	case "load":
		runLoadCmd(args)
	case "packet":
		runPacketCmd(args)
	case "conn":
		runConnCmd(args)
	case "bw":
		runBWCmd(args)
	case "cpu":
		runCPUCmd(args)
	case "fill":
		runFillCmd(args)
	case "disk":
		runDiskCmd(args)
	case "steal":
		runStealCmd(args)
	case "diag":
		runDiagCmd(args)
	case "inspect":
		runInspectCmd(args)
	case "all":
		runAllCmd(args)
	case "-h", "--help", "help":
		fmt.Print(usage())
	default:
		fmt.Fprintf(os.Stderr, T("unknown command %q\n\n"), cmd)
		fmt.Fprint(os.Stderr, usage())
		os.Exit(2)
	}
}
