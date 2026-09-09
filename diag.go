package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// 一键诊断:采样 CPU/内存/磁盘IO/socket/负载/GPU 等指标,
// 给出 p50/p95/p99、理想上限对比(占理想%)、以及"哪里负载最高"的结论。

type diskStat struct {
	reads, writes, msRead, msWrite, msIO uint64
}

func readDiskStats() map[string]diskStat {
	out := map[string]diskStat{}
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 14 {
			continue
		}
		name := f[2]
		// 跳过非真实块设备
		for _, p := range []string{"loop", "ram", "dm-", "zram", "md", "sr", "nbd"} {
			if strings.HasPrefix(name, p) {
				name = ""
				break
			}
		}
		if name == "" {
			continue
		}
		num := func(i int) uint64 { v, _ := strconv.ParseUint(f[i], 10, 64); return v }
		out[name] = diskStat{reads: num(3), msRead: num(6), writes: num(7), msWrite: num(10), msIO: num(12)}
	}
	return out
}

func readNetDev() (rx, tx uint64) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}
	for i, line := range strings.Split(string(b), "\n") {
		if i < 2 { // 前两行是表头
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		if strings.TrimSpace(line[:idx]) == "lo" {
			continue
		}
		f := strings.Fields(line[idx+1:])
		if len(f) < 10 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return
}

func readTCPInUse() int {
	// /proc/net/sock 在部分内核不存在;统计 /proc/net/tcp{,6} 里 ESTABLISHED(state=01) 的套接字
	n := 0
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if i == 0 {
				continue
			}
			f := strings.Fields(line)
			if len(f) >= 4 && f[3] == "01" {
				n++
			}
		}
	}
	return n
}

func readCtx() uint64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "ctxt ") {
			v, _ := strconv.ParseUint(strings.TrimSpace(line[5:]), 10, 64)
			return v
		}
	}
	return 0
}

func gpuInfo() string {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return "未检测到(无 nvidia-smi;云服务器一般无 GPU)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path,
		"--query-gpu=name,utilization.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return "读取失败: " + err.Error()
	}
	var parts []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(ln, ",")
		if len(f) >= 4 {
			parts = append(parts, fmt.Sprintf("%s: 利用率 %s%% | 显存 %s/%s MiB",
				strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.TrimSpace(f[2]), strings.TrimSpace(f[3])))
		}
	}
	if len(parts) == 0 {
		return "无数据"
	}
	return strings.Join(parts, " ; ")
}

// ---- API ----

type diagMetric struct {
	Name   string  `json:"name"`
	Unit   string  `json:"unit,omitempty"`
	Cur    float64 `json:"cur"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	Ideal  float64 `json:"ideal"` // 理想上限(0=仅参考,不评级)
	Ratio  float64 `json:"ratio"` // 当前/理想*100
	Status string  `json:"status"`
}

type diagResp struct {
	Sec     int          `json:"sec"`
	Samples int          `json:"samples"`
	NumCPU  int          `json:"num_cpu"`
	Top     string       `json:"top"`
	GPU     string       `json:"gpu"`
	RxKBps  float64      `json:"rx_kbps"`
	TxKBps  float64      `json:"tx_kbps"`
	Metrics []diagMetric `json:"metrics"`
}

func handleDiag(w http.ResponseWriter, r *http.Request) {
	sec := qInt(r, "sec", 10, 3, 300)
	n := 0
	var cpuU, cpuStl, memU, loadC, dUtil, dAwait, tcpE, ctxS []float64
	var rxRate, txRate float64

	var prevCT *cpuTimes
	prevDisk := map[string]diskStat{}
	var prevRx, prevTx, prevCtx uint64
	haveNet, haveCtx := false, false

	deadline := time.Now().Add(time.Duration(sec) * time.Second)
	for time.Now().Before(deadline) {
		ct, _ := readCPUTimes()
		total, avail := readMemKB()
		l1, _, _ := readLoadavg()
		disks := readDiskStats()
		rx, tx := readNetDev()
		tcp := readTCPInUse()
		ctx := readCtx()

		if prevCT != nil && ct.total() > prevCT.total() {
			dt := ct.total() - prevCT.total()
			pct := func(d uint64) float64 { return float64(d) / float64(dt) * 100 }
			busy := ct.user - prevCT.user + ct.nice - prevCT.nice + ct.system - prevCT.system +
				ct.irq - prevCT.irq + ct.softirq - prevCT.softirq
			cpuU = append(cpuU, pct(busy))
			cpuStl = append(cpuStl, pct(ct.steal-prevCT.steal))
		}
		if total > 0 {
			memU = append(memU, float64(total-avail)/float64(total)*100)
		}
		if nc := numCPU(); nc > 0 {
			loadC = append(loadC, l1/float64(nc))
		}
		// 磁盘:取 util 最高的那块盘
		bestUtil, bestAwait := 0.0, 0.0
		for name, d := range disks {
			p, ok := prevDisk[name]
			if !ok || d.msIO < p.msIO {
				continue
			}
			util := float64(d.msIO-p.msIO) / 10.0 // 1s 间隔下 msIO 差值/10 = util%
			iops := (d.reads - p.reads) + (d.writes - p.writes)
			await := 0.0
			if iops > 0 {
				await = float64((d.msRead-p.msRead)+(d.msWrite-p.msWrite)) / float64(iops)
			}
			if util > bestUtil {
				bestUtil, bestAwait = util, await
			}
		}
		dUtil = append(dUtil, bestUtil)
		dAwait = append(dAwait, bestAwait)
		if tcp >= 0 {
			tcpE = append(tcpE, float64(tcp))
		}
		if haveNet {
			rxRate = float64(rx-prevRx) / 1024
			txRate = float64(tx-prevTx) / 1024
		}
		if haveCtx {
			ctxS = append(ctxS, float64(ctx-prevCtx))
		}
		c := ct
		prevCT = &c
		prevDisk = disks
		prevRx, prevTx = rx, tx
		haveNet = true
		prevCtx = ctx
		haveCtx = true
		n++
		time.Sleep(time.Second)
	}

	mk := func(name, unit string, vals []float64, ideal float64) diagMetric {
		st := statsOf(vals)
		var cur float64
		if st.N > 0 {
			cur = vals[len(vals)-1]
		}
		m := diagMetric{Name: name, Unit: unit, Cur: r2(cur),
			P50: r2(st.P50), P95: r2(st.P95), P99: r2(st.P99), Ideal: ideal}
		if ideal > 0 {
			m.Ratio = r2(cur / ideal * 100)
			switch {
			case m.Ratio > 100:
				m.Status = "bad"
			case m.Ratio > 80:
				m.Status = "warn"
			default:
				m.Status = "ok"
			}
		} else {
			m.Status = "info"
		}
		return m
	}

	metrics := []diagMetric{
		mk("CPU 使用率", "%", cpuU, 70),
		mk("CPU steal 被偷取", "%", cpuStl, 1),
		mk("内存使用率", "%", memU, 80),
		mk("负载/核", "", loadC, 0.7),
		mk("磁盘 IO util", "%", dUtil, 50),
		mk("磁盘 await", "ms", dAwait, 5),
		mk("TCP 连接(ESTAB)", "条", tcpE, 1000),
		mk("上下文切换", "次/s", ctxS, 50000),
	}
	topName, topRatio := "—", -1.0
	for _, m := range metrics {
		if m.Ideal > 0 && m.Ratio > topRatio {
			topRatio, topName = m.Ratio, m.Name
		}
	}

	writeJSON(w, 200, diagResp{
		Sec: sec, Samples: n, NumCPU: numCPU(),
		Top:    fmt.Sprintf("%s(占理想上限 %.0f%%)", topName, topRatio),
		GPU:    gpuInfo(),
		RxKBps: r2(rxRate), TxKBps: r2(txRate),
		Metrics: metrics,
	})
}

// ---- CLI ----

func runDiagCmd(args []string) {
	fs := flag.NewFlagSet("diag", flag.ExitOnError)
	sec := fs.Int("d", 10, "采样秒数")
	tok := fs.String("token", "", "服务端令牌")
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	cl := newHTTPClient(2, 5*time.Minute)
	fmt.Printf("采样中(%d 秒)…\n", *sec)
	var d diagResp
	if err := getJSON(cl, withTok(base+"/api/diag?sec="+strconv.Itoa(*sec)), &d); err != nil {
		fail(err)
		return
	}
	printDiag(&d)
}

func printDiag(d *diagResp) {
	hdr(fmt.Sprintf("一键诊断(%ds 采样 %d 点 | %d 核)", d.Sec, d.Samples, d.NumCPU))
	fmt.Printf("  🔺 最大负载来源: %s\n\n", d.Top)
	fmt.Printf("  %-22s %10s %9s %9s %9s %9s %8s  %s\n",
		"指标", "当前", "p50", "p95", "p99", "理想上限", "占理想%", "状态")
	for _, m := range d.Metrics {
		name := m.Name
		if m.Unit != "" {
			name += "(" + m.Unit + ")"
		}
		ideal, ratio := "参考", "—"
		if m.Ideal > 0 {
			ideal = fmt.Sprintf("%g", m.Ideal)
			ratio = fmt.Sprintf("%.0f%%", m.Ratio)
		}
		st := map[string]string{"ok": "✓", "warn": "△ 偏高", "bad": "⚠ 超标"}[m.Status]
		if st == "" {
			st = "·"
		}
		fmt.Printf("  %-22s %10.2f %9.2f %9.2f %9.2f %9s %8s  %s\n",
			name, m.Cur, m.P50, m.P95, m.P99, ideal, ratio, st)
	}
	fmt.Printf("\n  GPU      : %s\n", d.GPU)
	fmt.Printf("  网络吞吐 : 收 %.0f KB/s | 发 %.0f KB/s(参考,不计入排名)\n", d.RxKBps, d.TxKBps)
	fmt.Println("  说明: 占理想% = 当前/理想上限;>80% 偏高,>100% 超标。理想值参考:CPU≤70%")
	fmt.Println("        steal≤1% 内存≤80% 负载≤0.7/核 磁盘util≤50% await≤5ms TCP≤1000 切换≤5万/s")
}
