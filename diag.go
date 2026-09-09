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
// 指标用稳定的 key 标识(英文),CLI/网页各自按语言渲染标签。

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
		return T("not detected (no nvidia-smi; usually no GPU on cloud VPS)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path,
		"--query-gpu=name,utilization.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits").Output()
	if err != nil {
		return TF("read failed: %v", err)
	}
	var parts []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(ln, ",")
		if len(f) >= 4 {
			parts = append(parts, TF("%s: util %s%% | vram %s/%s MiB",
				strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.TrimSpace(f[2]), strings.TrimSpace(f[3])))
		}
	}
	if len(parts) == 0 {
		return T("no data")
	}
	return strings.Join(parts, " ; ")
}

// ---- API ----

type diagMetric struct {
	Key    string  `json:"key"`
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
	TopKey  string       `json:"top_key"`
	TopPct  float64      `json:"top_pct"`
	GPU     string       `json:"gpu"`
	RxKBps  float64      `json:"rx_kbps"`
	TxKBps  float64      `json:"tx_kbps"`
	Metrics []diagMetric `json:"metrics"`
}

// 指标 key → 英文标签(zhDict 提供中文),顺序即展示顺序
var diagLabels = []struct{ Key, En, Unit string }{
	{"cpu_util", "CPU usage", "%"},
	{"cpu_steal", "CPU steal (stolen)", "%"},
	{"mem_used", "memory used", "%"},
	{"load_per_core", "load per core", ""},
	{"disk_util", "disk IO util", "%"},
	{"disk_await", "disk await", "ms"},
	{"tcp_est", "TCP conns (ESTAB)", "conn"},
	{"ctx_switch", "context switches", "/s"},
}

func diagLabel(key string) string {
	for _, l := range diagLabels {
		if l.Key == key {
			return T(l.En)
		}
	}
	return key
}

var diagIdeals = map[string]float64{
	"cpu_util": 70, "cpu_steal": 1, "mem_used": 80, "load_per_core": 0.7,
	"disk_util": 50, "disk_await": 5, "tcp_est": 1000, "ctx_switch": 50000,
}

func handleDiag(w http.ResponseWriter, r *http.Request) {
	sec := qInt(r, "sec", 10, 3, 300)
	n := 0
	series := map[string][]float64{}
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
			series["cpu_util"] = append(series["cpu_util"], pct(busy))
			series["cpu_steal"] = append(series["cpu_steal"], pct(ct.steal-prevCT.steal))
		}
		if total > 0 {
			series["mem_used"] = append(series["mem_used"], float64(total-avail)/float64(total)*100)
		}
		if nc := numCPU(); nc > 0 {
			series["load_per_core"] = append(series["load_per_core"], l1/float64(nc))
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
		series["disk_util"] = append(series["disk_util"], bestUtil)
		series["disk_await"] = append(series["disk_await"], bestAwait)
		series["tcp_est"] = append(series["tcp_est"], float64(tcp))
		if haveNet {
			rxRate = float64(rx-prevRx) / 1024
			txRate = float64(tx-prevTx) / 1024
		}
		if haveCtx {
			series["ctx_switch"] = append(series["ctx_switch"], float64(ctx-prevCtx))
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

	var metrics []diagMetric
	for _, l := range diagLabels {
		vals := series[l.Key]
		st := statsOf(vals)
		var cur float64
		if st.N > 0 {
			cur = vals[len(vals)-1]
		}
		m := diagMetric{Key: l.Key, Unit: l.Unit, Cur: r2(cur),
			P50: r2(st.P50), P95: r2(st.P95), P99: r2(st.P99), Ideal: diagIdeals[l.Key]}
		if m.Ideal > 0 {
			m.Ratio = r2(cur / m.Ideal * 100)
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
		metrics = append(metrics, m)
	}
	topKey, topPct := "", -1.0
	for _, m := range metrics {
		if m.Ideal > 0 && m.Ratio > topPct {
			topPct, topKey = m.Ratio, m.Key
		}
	}

	writeJSON(w, 200, diagResp{
		Sec: sec, Samples: n, NumCPU: numCPU(),
		TopKey: topKey, TopPct: r2(topPct),
		GPU:    gpuInfo(),
		RxKBps: r2(rxRate), TxKBps: r2(txRate),
		Metrics: metrics,
	})
}

// ---- CLI ----

func runDiagCmd(args []string) {
	fs := flag.NewFlagSet("diag", flag.ExitOnError)
	sec := fs.Int("d", 10, T("sampling seconds"))
	tok := fs.String("token", "", T("server token"))
	fs.Parse(reorderArgs(fs, args))
	base := targetURL(fs)
	clientToken = *tok
	cl := newHTTPClient(2, 5*time.Minute)
	fmt.Printf(T("sampling (%d s)...\n"), *sec)
	var d diagResp
	if err := getJSON(cl, withTok(base+"/api/diag?sec="+strconv.Itoa(*sec)), &d); err != nil {
		fail(err)
		return
	}
	printDiag(&d)
}

func printDiag(d *diagResp) {
	hdr(TF("Quick diagnosis (%ds, %d samples | %d cores)", d.Sec, d.Samples, d.NumCPU))
	fmt.Printf("  "+T("🔺 highest-load source: %s (%.0f%% of ideal cap)")+"\n", diagLabel(d.TopKey), d.TopPct)
	fmt.Println()
	fmt.Printf("  %-22s %10s %9s %9s %9s %9s %8s  %s\n",
		T("metric"), T("cur"), "p50", "p95", "p99", T("ideal cap"), T("of ideal %"), T("status"))
	for _, m := range d.Metrics {
		name := diagLabel(m.Key)
		if m.Unit != "" {
			name += "(" + m.Unit + ")"
		}
		ideal, ratio := T("ref"), "—"
		if m.Ideal > 0 {
			ideal = fmt.Sprintf("%g", m.Ideal)
			ratio = fmt.Sprintf("%.0f%%", m.Ratio)
		}
		st := map[string]string{"ok": "✓", "warn": T("△ high"), "bad": T("⚠ over")}[m.Status]
		if st == "" {
			st = "·"
		}
		fmt.Printf("  %-22s %10.2f %9.2f %9.2f %9.2f %9s %8s  %s\n",
			name, m.Cur, m.P50, m.P95, m.P99, ideal, ratio, st)
	}
	fmt.Printf("\n  "+T("GPU      : %s\n"), d.GPU)
	fmt.Printf("  "+T("network  : rx %.0f KB/s | tx %.0f KB/s (reference)\n"), d.RxKBps, d.TxKBps)
	fmt.Println("  " + T("Note: of-ideal% = current/ideal cap; >80% high, >100% over."))
	fmt.Println("  " + T("ideals: CPU≤70% steal≤1% mem≤80% load≤0.7/core diskutil≤50% await≤5ms TCP≤1000 ctx≤50k/s"))
}
