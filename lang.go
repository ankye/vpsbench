package main

// 轻量 i18n:默认英语(避免不支持中文的终端乱码),-lang zh 或 VPSBENCH_LANG=zh 切中文。
// 代码里的字符串即英文原文(也是 dict 的 key),zhDict 提供中文翻译,未命中时回退英文。

import (
	"fmt"
	"os"
	"strings"
)

var langName = "en"

func T(s string) string {
	if langName != "zh" {
		return s
	}
	if v, ok := zhDict[s]; ok {
		return v
	}
	return s
}

func TF(s string, args ...any) string { return fmt.Sprintf(T(s), args...) }

func SetLang(l string) {
	l = strings.ToLower(strings.TrimSpace(l))
	if strings.HasPrefix(l, "zh") || l == "cn" || l == "chinese" {
		langName = "zh"
	} else {
		langName = "en"
	}
}

// extractLang 从参数中取出 -lang xx / -lang=xx 并从参数里移除,返回 (值, 剩余参数)
func extractLang(args []string) (string, []string) {
	val := ""
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-lang" || a == "--lang" {
			if i+1 < len(args) {
				val = args[i+1]
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "-lang=") || strings.HasPrefix(a, "--lang=") {
			val = a[strings.Index(a, "=")+1:]
			continue
		}
		out = append(out, a)
	}
	return val, out
}

// InitLang 在子命令分发前调用:命令行 -lang 优先,其次环境变量 VPSBENCH_LANG,默认英语
func InitLang(args []string) []string {
	l, rest := extractLang(args)
	if l == "" {
		l = os.Getenv("VPSBENCH_LANG")
	}
	SetLang(l)
	return rest
}

var zhDict = map[string]string{
	// ---- 通用 ----
	"Error: exactly one <url> argument is required, e.g. http://1.2.3.4:8300": "错误: 需要且只需要一个 <url> 参数,例如 http://1.2.3.4:8300",
	"✗ failed:":                        "✗ 失败:",
	"test duration":                    "测试时长",
	"send interval":                    "发包间隔",
	"concurrency":                      "并发数",
	"duration":                         "测试时长",
	"server token":                     "服务端令牌",
	"ms per run":                       "每轮时长(ms)",
	"runs, keep best":                  "轮数,取最优",
	"cores to fill (0=all)":            "打满核数(0=服务端全部核)",
	"duration in ms":                   "持续时长(ms)",
	"sequential MB":                    "顺序读写 MB",
	"small file count (fsync samples)": "小文件个数(fsync 延迟样本)",
	"parallel streams":                 "并行流数",
	"test upload (default: download)":  "测上行(默认测下行)",

	// ---- ping ----
	"Latency test %s (interval %s, ~%.0f probes)": "延迟测试 %s(间隔 %s,共约 %.0f 个包)",
	"no successful requests (%d errors)":          "无成功请求(错误 %d 次)",
	"ok / failed":                                 "成功/失败",
	"RTT max":                                     "RTT 最大",
	"worst spikes:":                               "最差尖刺:",
	"⚠ p99 ≫ p50: periodic stalls on path or peer (neighbor steal / jitter)": "⚠ p99 显著高于 p50:链路或对端存在周期性卡顿(可能是邻居偷取/网络抖动)",

	// ---- load ----
	"response packet size in bytes (0=tiny ping), e.g. 1024/2048/4096": "响应包大小(字节,0=极小 ping 包);如 1024/2048/4096",
	"Load test %s (%d conns / %s)":                                     "并发压测 %s(%d 并发 / %s)",
	"total requests":                                                   "总请求",
	"errors":                                                           "错误",
	"latency p50 / p95 / p99":                                          "延迟 p50 / p95 / p99",
	"latency max":                                                      "延迟最大",
	"throughput":                                                       "吞吐",
	"%.1f MB/s (≈ %.0f Mbps)":                                          "%.1f MB/s(≈ %.0f Mbps)",
	"server view":                                                      "服务端视角",

	// ---- packet ----
	"duration per packet size":                            "每个包尺寸的测试时长",
	"comma-separated packet sizes in bytes (≤65536)":      "逗号分隔的包大小(字节,≤65536)",
	"Error: invalid sizes, e.g. -sizes 1024,2048,4096":    "错误: sizes 参数无效,例如 -sizes 1024,2048,4096",
	"Packet-size concurrency ladder (%d conns × %s each)": "包尺寸并发阶梯(%d 并发 × %s/档)",
	"pkt":              "包",
	"err%":             "错误%",
	"—— all failed ——": "—— 全部失败 ——",
	"Analysis: RPS drops sharply with size (%.0f→%.0f) — bandwidth/kernel-buffer bound; watch throughput for big packets, not RPS": "分析: RPS 随包增大急剧下降(%.0f→%.0f)——带宽/内核缓冲瓶颈,大包场景看吞吐而非 RPS",
	"Analysis: RPS barely changes with size (%.0f→%.0f) — request-processing/CPU bound; packet size is not the bottleneck":         "分析: RPS 基本不随包大小变化(%.0f→%.0f)——请求处理/CPU 瓶颈,包尺寸不是短板",
	"Analysis: RPS drops moderately (%.0f→%.0f) — bandwidth and processing both contribute":                                        "分析: RPS 中等下降(%.0f→%.0f)——带宽与请求处理共同作用",

	// ---- conn ----
	"target concurrent connections":                                 "目标并发连接数",
	"hold time per connection":                                      "每个连接保持时长",
	"new connections per wave":                                      "每波新增连接数",
	"Max concurrent connections (target %d, +%d per wave, hold %s)": "最大并发连接测试(目标 %d,每波 +%d,保持 %s)",
	"  launched %4d, ok %d, failed %d\n":                            "  已发起 %4d,成功 %d,失败 %d\n",
	"all launched, holding %s then releasing...":                    "全部发起完毕,保持 %s 后释放...",
	"established": "成功建立",
	"failed":      "失败",
	"✓ reached target; raise -c to probe the ceiling":  "✓ 达到目标并发数,可加大 -c 继续探测上限",
	"✗ below target: conntrack / fd limit / firewall?": "✗ 未达目标:可能受 conntrack / 文件描述符 / 防火墙限制",

	// ---- bw ----
	"Download":                            "下行",
	"Upload":                              "上行",
	"%s bandwidth test (%d streams / %s)": "%s带宽测试(%d 并行流 / %s)",
	"total transferred":                   "总流量",
	"%.1f MB":                             "%.1f MB",

	// ---- cpu ----
	"Single-core benchmark (%d runs × %dms, best)": "单核算力测试(%d 轮 × %dms,取最优)",
	"  run #%d: %.2f M ops/s (wall %s)\n":          "  第 %d 轮: %.2f M ops/s(墙钟 %s)\n",
	"single-core":                                  "单核算力",
	"%.2f M ops/s":                                 "%.2f M ops/s",

	// ---- fill ----
	"Full-load test: fill %d cores × %dms (server has %d cores)": "满载测试:打满 %d 核 × %dms(服务端标称 %d 核)",
	"  running...\n":                                                    "  运行中...\n",
	"actually got":                                                      "实际拿到",
	"%.2f cores × %.1fs = %.2f CPU-seconds":                             "%.2f 核 × %.1fs = %.2f CPU 秒",
	"core delivery":                                                     "核数兑现率",
	"%.1f%% (%.2f of %d cores actually usable)":                         "%.1f%%(%d 核里实际可用 %.2f 核)",
	"per-core decay at full load":                                       "满载单核衰减",
	"%.0f%% (idle %.1f M/s → loaded %.1f M/s)":                          "%.0f%%(空载 %.1f M/s → 满载 %.1f M/s)",
	"✓ real cores: basically dedicated, no oversell/throttle":           "✓ 核数真实:基本独享,无超售/限速",
	"△ mild contention: neighbors or tight cgroup quota":                "△ 轻度争抢:有邻居或 cgroup 配额略紧",
	"⚠ clear oversell: less than 85%% of advertised cores":              "⚠ 明显超售:标称核数兑现不足 85%",
	"✗ severe oversell or hard throttle: <70%%, definitely shared host": "✗ 严重超售或强限速:兑现不足 70%,共享宿主机无疑",

	// ---- disk ----
	"Disk test (seq %d MB + %d small-file fsync)":            "磁盘测试(顺序 %d MB + %d 个小文件 fsync)",
	"sequential write":                                       "顺序写",
	"%s (%.0f MB/s)":                                         "%s(%.0f MB/s)",
	"large-file fsync":                                       "大文件 fsync",
	"sequential read (cached)":                               "顺序读(含缓存)",
	"small-file fsync p50/p95/p99":                           "小文件 fsync p50/p95/p99",
	"small-file fsync max":                                   "小文件 fsync 最大",
	"✓ excellent storage (local NVMe class)":                 "✓ 存储优秀(本地 NVMe 级)",
	"✓ good storage":                                         "✓ 存储良好",
	"△ mediocre: network storage or neighbor I/O contention": "△ 存储一般:网络存储或邻居 I/O 争抢",
	"⚠ high storage latency: heavy I/O contention or oversell — painful for DB/logs": "⚠ 存储延迟高:严重 I/O 争抢或超售,建库/写日志会痛",

	// ---- steal ----
	"watch mode, refresh every 5s": "持续观察模式,每 5s 刷新一行",
	"watching... (Ctrl+C to quit)": "持续观察中(Ctrl+C 退出)...",
	"  fetch failed:":              "  拉取失败:",
	"[%s] steal: cur %.1f%% avg %.1f%% max %.1f%% | jitter p99 %s max %s | load %.2f\n": "[%s] steal: 当前 %.1f%% 均值 %.1f%% 峰值 %.1f%% | 抖动 p99 %s 峰值 %s | 负载 %.2f\n",
	"Server steal report (shared-host detection)":                                       "服务端资源偷取报告(共享宿主机检测)",
	"host":                              "主机",
	"%s | %d cores | monitoring for %s": "%s | %d 核 | 监控已运行 %s",
	"memory":                            "内存",
	"%d MB avail / %d MB":               "%d MB 可用 / %d MB",
	"load (1/5/15m)":                    "负载(1/5/15m)",
	"CPU steal%":                        "CPU steal%",
	"cur %.1f | avg %.1f | p95 %.1f | max %.1f": "当前 %.1f | 平均 %.1f | p95 %.1f | 最大 %.1f",
	"sched jitter (base %dms)":                  "调度抖动(基准 %dms)",
	"p50 %s | p95 %s | p99 %s | max %s":         "p50 %s | p95 %s | p99 %s | 最大 %s",
	"jitter over-limit count":                   "抖动超限次数",
	">10ms: %d | >50ms: %d | >100ms: %d":        ">10ms: %d | >50ms: %d | >100ms: %d",
	"ℹ steal always 0: some virtualization (OpenVZ/LXC) hides steal — judge by scheduling jitter": "ℹ steal 恒为 0:部分虚拟化(如 OpenVZ/LXC)看不到 steal,请以调度抖动为准",
	"steal/jitter events (recent):": "偷取事件(最近):",
	"    %s  jitter %s\n":           "    %s  抖动 %s\n",
	"ℹ server's own CPU was busy recently (load test?): jitter/load inflated, retest when idle": "ℹ 近期服务端自身 CPU 较高(可能刚跑过压测),抖动/负载数据会偏高,空闲时复测更准",
	"Verdict ✓ no steal while idle: host not crowded (watch 24h to confirm peak hours)":         "结论 ✓ 空闲期无偷取迹象:宿主机不挤(建议连续观察 24h 复核高峰时段)",
	"Verdict △ mild contention: neighbors exist but impact is contained":                        "结论 △ 轻度争抢:有邻居但影响可控",
	"Verdict ⚠ obvious steal: heavily oversold host — think twice for latency-sensitive work":   "结论 ⚠ 偷取明显:宿主机超售严重,延迟敏感业务慎选",

	// ---- all ----
	"load-test connections":         "并发压测连接数",
	"load-test duration":            "压测时长",
	"latency-test duration":         "延迟测试时长",
	"bandwidth-test duration":       "带宽测试时长",
	"full-load test duration in ms": "满载测试时长(ms)",
	"✗ cannot reach server: %v":     "✗ 连接服务端失败: %v",
	"  check: server running? port open (ufw allow 8300)? -token match?": "  检查:服务端是否启动、端口是否放行(ufw allow 8300)、-token 是否匹配",
	"target machine info":           "被测机器信息",
	"hostname":                      "主机名",
	"%d cores":                      "%d 核",
	"kernel":                        "内核",
	"uptime":                        "开机时长",
	"Server summary All-in-One":     "服务器信息汇总 All-in-One",
	"system":                        "系统",
	"CPU power":                     "CPU 算力",
	"single-core %.2f M ops/s %s\n": "单核 %.2f M ops/s %s\n",
	"core delivery: %d advertised → %.2f actual (%.0f%%) %s\n":                 "核数兑现: 标称 %d 核 → 实际 %.2f 核(%.0f%%)%s\n",
	"disk: write %.0f MB/s | small-file fsync p99 %s %s\n":                     "磁盘: 写 %.0f MB/s | 小文件 fsync p99 %s %s\n",
	"steal: avg %.1f%% | peak %.1f%% %s\n":                                     "偷取 steal: 平均 %.1f%% | 峰值 %.1f%% %s\n",
	"sched jitter: p99 %s | peak %s %s\n":                                      "调度抖动: p99 %s | 峰值 %s %s\n",
	"network: RTT p50 %s | p99 %s\n":                                           "网络: RTT p50 %s | p99 %s\n",
	"concurrency: %.0f req/s @ %d conns | errors %.2f%% %s\n":                  "并发: %.0f req/s @ %d 并发 | 错误 %.2f%% %s\n",
	"downlink: %.1f MB/s (≈%.0f Mbps)%s\n":                                     "下行带宽: %.1f MB/s(≈%.0f Mbps)%s\n",
	"Tips: run `vpsbench steal <url> -w` for 24h to catch peak-hour steal;":    "建议: `vpsbench steal <url> -w` 挂 24h 抓高峰偷取;",
	"      `vpsbench packet <url> -c 100` for 1K/2K/4K packet concurrency;":    "      `vpsbench packet <url> -c 100` 测 1K/2K/4K 包并发;",
	"      `vpsbench conn <url> -c 1000` to probe max concurrent connections.": "      `vpsbench conn <url> -c 1000` 探最大并发连接数。",

	// ---- diag ----
	"sampling (%d s)...\n":                            "采样中(%d 秒)...\n",
	"Quick diagnosis (%ds, %d samples | %d cores)":    "一键诊断(%ds 采样 %d 点 | %d 核)",
	"🔺 highest-load source: %s (%.0f%% of ideal cap)": "🔺 最大负载来源: %s(占理想上限 %.0f%%)",
	"metric":             "指标",
	"cur":                "当前",
	"ideal cap":          "理想上限",
	"of ideal %":         "占理想%",
	"status":             "状态",
	"✓":                  "✓",
	"△ high":             "△ 偏高",
	"⚠ over":             "⚠ 超标",
	"ref":                "参考",
	"CPU usage":          "CPU 使用率",
	"CPU steal (stolen)": "CPU steal 被偷取",
	"memory used":        "内存使用率",
	"load per core":      "负载/核",
	"disk IO util":       "磁盘 IO util",
	"disk await":         "磁盘 await",
	"TCP conns (ESTAB)":  "TCP 连接(ESTAB)",
	"context switches":   "上下文切换",
	"GPU      : %s\n":    "GPU      : %s\n",
	"network  : rx %.0f KB/s | tx %.0f KB/s (reference)\n":                                         "网络吞吐 : 收 %.0f KB/s | 发 %.0f KB/s(参考)\n",
	"Note: of-ideal%% = current/ideal cap; >80%% high, >100%% over.":                               "说明: 占理想%% = 当前/理想上限;>80%% 偏高,>100%% 超标。",
	"ideals: CPU≤70%% steal≤1%% mem≤80%% load≤0.7/core diskutil≤50%% await≤5ms TCP≤1000 ctx≤50k/s": "理想值: CPU≤70% steal≤1% 内存≤80% 负载≤0.7/核 磁盘util≤50% await≤5ms TCP≤1000 切换≤5万/s",
	"not detected (no nvidia-smi; usually no GPU on cloud VPS)":                                    "未检测到(无 nvidia-smi;云服务器一般无 GPU)",
	"read failed: %v":                "读取失败: %v",
	"no data":                        "无数据",
	"%s: util %s%% | vram %s/%s MiB": "%s: 利用率 %s%% | 显存 %s/%s MiB",

	// ---- server startup ----
	"vpsbench server started (Gin + embedded web UI)": "vpsbench 服务端已启动(Gin + 内嵌网页)",
	"listen":                  "监听",
	"cores":                   "核",
	"disk dir":                "磁盘目录",
	"token":                   "令牌",
	"not set (public access)": "未设置(公开访问)",
	"enabled":                 "已启用",
	"▶ open in browser  http://<public-ip>%s   — click buttons to test":                 "▶ 浏览器打开  http://<本机公网IP>%s  点按钮即可测试",
	"▶ from LAN/CLI   : ./vpsbench all http://<ip>%s":                                   "▶ 内网/命令行: ./vpsbench all http://<本机IP>%s",
	"Note: open the port in firewall (e.g. ufw allow 8300); stop the server when done.": "提示: 防火墙记得放行端口(如 ufw allow 8300);测完记得关服务",
	"unknown command %q\n\n": "未知命令 %q\n\n",
	"sampling seconds":       "采样秒数",
	"max":                    "最大",
	"CPU steal %.1f%% detected (host gave CPU to neighbors)":              "检测到 CPU steal %.1f%%(宿主机把 CPU 偷给了邻居)",
	"scheduling jitter spike %.1fms (base 10ms, likely steal/contention)": "检测到调度抖动尖刺 %.1fms(基准 10ms,疑似被偷取/争抢)",

	// ---- inspect 一键验货 ----
	"next step": "回车继续",
	"quit":      "q 退出",
	"virtualization: %s (container — steal is invisible, ceiling is limited)": "虚拟化: %s(容器——看不到 steal,性能上限受限)",
	"virtualization: %s":                                                            "虚拟化: %s",
	"CPU: %s | %d vCPU":                                                             "CPU: %s | %d vCPU",
	"cores match order: %d = %d":                                                    "核数与订单一致: %d = %d",
	"cores mismatch: ordered %d, got %d":                                            "核数不符: 订单 %d 核,实际 %d 核",
	"memory: %d MB total, %d MB available, swap %d MB":                              "内存: 共 %d MB,可用 %d MB,swap %d MB",
	"RAM matches order: %dG ≈ %dG":                                                  "内存与订单一致: %dG ≈ %dG",
	"RAM mismatch: ordered %dG, got %dG":                                            "内存不符: 订单 %dG,实际 %dG",
	"root disk: %d GiB":                                                             "根磁盘: %d GiB",
	"disk matches order: %dG ≈ %dG":                                                 "磁盘与订单一致: %dG ≈ %dG",
	"disk mismatch: ordered %dG, got %dG":                                           "磁盘不符: 订单 %dG,实际 %dG",
	"swap ≥ half of RAM (%d MB) — some providers fake RAM with swap":                "swap ≥ 内存一半(%d MB)——有厂商用 swap 冒充内存",
	"OS: %s | %s | up %s":                                                           "系统: %s | %s | 开机 %s",
	"no AES-NI — crypto/VPN throughput will suffer":                                 "无 AES-NI——加密/VPN 吞吐会受影响",
	"AES-NI available":                                                              "AES-NI 可用",
	"single-core: %.0f M ops/s (typical VPS: 100–400)":                              "单核: %.0f M ops/s(主流 VPS 约 100–400)",
	"full-load: %.2f/%d cores delivered (%.0f%%)":                                   "满载: %.2f/%d 核兑现(%.0f%%)",
	"per-core under load: %.0f%% of idle":                                           "满载单核保留空载算力的 %.0f%%",
	"cores are real — no oversell/throttle":                                         "核数真实——无超售/限速",
	"mild contention: %.0f%% delivered":                                             "轻度争抢: 兑现 %.0f%%",
	"oversold/throttled: only %.0f%% of advertised cores":                           "超售/限速: 标称核数只兑现 %.0f%%",
	"not enough free memory to test (%d MB)":                                        "可用内存不足,跳过测试(%d MB)",
	"memory bandwidth: %d MB touched in %s → %.1f GB/s":                             "内存带宽: %d MB 写入耗时 %s → %.1f GB/s",
	"memory bandwidth is low (%.1f GB/s) — THP/swap-backed memory?":                 "内存带宽偏低(%.1f GB/s)——THP/swap 兜底内存?",
	"memory bandwidth normal":                                                       "内存带宽正常",
	"root disk type: SSD/NVMe (non-rotational)":                                     "根磁盘类型: SSD/NVMe(非机械)",
	"root disk is ROTATIONAL (HDD) — IO latency will be high":                       "根磁盘是机械盘(HDD)——IO 延迟会很高",
	"root disk type: unknown":                                                       "根磁盘类型: 未知",
	"disk test failed (permissions?)":                                               "磁盘测试失败(权限?)",
	"seq write: %.0f MB/s | 64MB fsync: %s":                                         "顺序写: %.0f MB/s | 64MB fsync: %s",
	"small-file fsync p50 %s / p99 %s / max %s":                                     "小文件 fsync p50 %s / p99 %s / 最大 %s",
	"storage latency good":                                                          "存储延迟良好",
	"storage latency mediocre — network storage or noisy neighbors":                 "存储延迟一般——网络存储或邻居争抢",
	"storage latency is HIGH (p99 %s) — heavily contended IO":                       "存储延迟高(p99 %s)——IO 严重争抢",
	"public IP: %s (%s, %s) %s":                                                     "公网 IP: %s(%s,%s)%s",
	"public IP lookup skipped (no outbound https?)":                                 "公网 IP 查询跳过(无出网 https?)",
	"IPv6 available":                                                                "IPv6 可用",
	"no global IPv6":                                                                "无公网 IPv6",
	"DNS resolution failed: %v":                                                     "DNS 解析失败: %v",
	"DNS resolution ok":                                                             "DNS 解析正常",
	"public downlink: %.1f MB/s ≈ %.0f Mbps (cloudflare)":                           "公网下行: %.1f MB/s ≈ %.0f Mbps(cloudflare)",
	"public downlink is slow (<40 Mbps)":                                            "公网下行偏慢(<40 Mbps)",
	"loopback HTTP: %.0f req/s @16 conns (local stack)":                             "回环 HTTP: %.0f req/s @16 并发(本机协议栈)",
	"loopback RPS is low — check CPU/governor":                                      "回环 RPS 偏低——检查 CPU/调度器",
	"idle sampling":                                                                 "空闲采样中",
	"steal%%: avg %.1f | peak %.1f":                                                 "steal%%: 平均 %.1f | 峰值 %.1f",
	"jitter (10ms base): p50 %s | p99 %s | peak %s | >10ms ×%d":                     "抖动(基准10ms): p50 %s | p99 %s | 峰值 %s | >10ms ×%d",
	"no steal while idle — host is not crowded":                                     "空闲期无偷取——宿主机不挤",
	"mild steal/contention detected":                                                "检测到轻度偷取/争抢",
	"obvious steal — heavily oversold host":                                         "偷取明显——宿主机严重超售",
	"tip: 15s is a snapshot — run `vpsbench steal -w` for 24h to catch peaks":       "提示: 15s 只是快照——用 `vpsbench steal -w` 挂 24h 抓高峰",
	"root + password login both enabled — brute-forceable; use keys or disable one": "root+密码登录同时开启——易被爆破;改用密钥或关掉其一",
	"not available (not root?)":                                                     "不可用(非 root?)",
	"%d non-root users with UID 0!":                                                 "有 %d 个非 root 的 UID 0 用户!",
	"no unexpected UID-0 users":                                                     "无异常 UID 0 用户",
	"%d failed-login records (lastb) — internet noise; install fail2ban":            "%d 条失败登录记录(lastb)——公网噪音;建议装 fail2ban",
	"%d listening TCP ports":                                                        "%d 个监听中的 TCP 端口",
	"firewall: ufw active":                                                          "防火墙: ufw 已启用",
	"firewall: ufw INACTIVE — exposure risk":                                        "防火墙: ufw 未启用——裸奔风险",
	"firewall: iptables present (rules not verified)":                               "防火墙: 有 iptables(规则未核验)",
	"no firewall tooling found (ufw/iptables)":                                      "未发现防火墙工具(ufw/iptables)",
	"%d packages upgradable — patch soon (apt upgrade)":                             "%d 个包可升级——尽快打补丁(apt upgrade)",
	"all packages up to date":                                                       "所有包已是最新",
	"auto-continue, no pauses (for scripts/CI)":                                     "自动连续执行,不暂停(脚本/CI 用)",
	"ordered CPU cores, e.g. 2 (0=skip check)":                                      "订单 CPU 核数,如 2(0=跳过)",
	"ordered RAM GB, e.g. 4 (0=skip)":                                               "订单内存 GB,如 4(0=跳过)",
	"ordered disk GB, e.g. 80 (0=skip)":                                             "订单磁盘 GB,如 80(0=跳过)",
	"report file (default ~/vpsbench-inspect-<host>-<date>.txt, empty=auto)":        "报告文件(默认 ~/vpsbench-inspect-<主机>-<日期>.txt,留空自动)",
	"Step 1 · Specs & order match":                                                  "第 1 步 · 配置核对(与订单比对)",
	"Step 2 · CPU reality & oversell":                                               "第 2 步 · CPU 真伪与超售",
	"Step 3 · Memory":                                                               "第 3 步 · 内存",
	"Step 4 · Disk & IO":                                                            "第 4 步 · 磁盘与 IO",
	"Step 5 · Network":                                                              "第 5 步 · 网络",
	"Step 6 · Idle steal (the core check)":                                          "第 6 步 · 空闲偷取检测(核心)",
	"Step 7 · Security baseline":                                                    "第 7 步 · 安全基线",
	"vpsbench one-click acceptance inspection":                                      "vpsbench 一键验货",
	"inspection aborted":                                                            "验货已中止",
	"ACCEPTED — good to go":                                                         "验收通过——可上线",
	"REJECTED — open a ticket / refund":                                             "验收不通过——建议工单/退款",
	"ACCEPTED WITH NOTES — %d warnings, fix when convenient":                        "有保留通过——%d 项警告,择机处理",
	"Acceptance summary":                                                            "验收单",
	"Verdict":                                                                       "结论",
	"report saved":                                                                  "报告已保存",

	// ---- 免 server 自测 ----
	"no URL given — testing this machine via an in-process server (loopback)": "未给 URL——进程内起服务自测本机(回环)",
	"✗ cannot start embedded server:":                                         "✗ 无法启动进程内服务:",
}
