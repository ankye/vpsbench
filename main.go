package main

import (
	"fmt"
	"os"
)

const usage = `vpsbench — VPS 性能 / 并发 / 超售(共享宿主机偷取)检测工具
单二进制,两种用法:
  1) Gin 服务器 + 内嵌网页:server 跑在被测 VPS 上,浏览器打开直接点按钮测试
  2) CLI 客户端:内网/无浏览器环境用命令行压测同一套 API

服务端(跑在被测 VPS 上):
  vpsbench server [-addr :8300] [-dir /tmp] [-token XXX]   启动测试服务(Gin + 网页)

客户端(跑在你本地或另一台机器):
  vpsbench ping  <url> [-d 30s] [-i 100ms]     延迟测试:RTT 抖动,检测邻居偷取导致的延迟尖刺
  vpsbench load  <url> [-c 50] [-d 30s] [-size 0]  HTTP 并发压测:RPS / 成功率 / 分位延迟(size=响应包字节)
  vpsbench packet <url> [-c 100] [-d 10s] [-sizes 1024,2048,4096]  包尺寸并发阶梯(1K/2K/4K)
  vpsbench conn  <url> [-c 500] [-hold 30s]    最大并发连接数测试(TCP 并发承受力)
  vpsbench bw    <url> [-d 10s] [-c 4] [-up]   下行/上行带宽测试
  vpsbench cpu   <url> [-ms 2000]              单核算力测试(跑 3 轮取最优)
  vpsbench fill  <url> [-n 0全部核] [-ms 5000] 打满 n 核:检测核数超售 / cgroup 限速 / 降频
  vpsbench disk  <url> [-mb 64] [-files 100]   磁盘:顺序读写带宽 + 小文件 fsync 延迟(I/O 被偷取最敏感)
  vpsbench steal <url> [-w]                    查询服务端 CPU steal / 调度抖动报告(-w 持续观察)
  vpsbench diag  <url> [-d 10]                 一键诊断:CPU/GPU/IO/socket/mem/磁盘 谁负载最高+p50/p95/p99+理想值对比
  vpsbench all   <url>                         全套测试 + 综合结论

示例:
  服务端:  ./vpsbench server -addr :8300            # 记得防火墙放行端口
  客户端:  ./vpsbench all http://<vps-ip>:8300
  长期蹲守偷取: ./vpsbench steal http://<vps-ip>:8300 -w

关于"共享主机被偷取":
  - steal%:   虚拟化层承认的被其他租户占走的 CPU 时间占比(/proc/stat)
  - 调度抖动: 进程 sleep 10ms 的实际耗时偏差——被偷取造成的真实延迟体现
  - 空闲时段抖动大 / steal 高 ⇒ 宿主机超售,邻居在抢资源
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
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
	case "all":
		runAllCmd(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}
