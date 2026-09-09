package main

// 一键验货(inspect):新 VPS 到货验收向导。
// 8 步检测,每步展示结果后回车继续(一路 next),最后输出验收单并可存档。
// 非交互环境(管道/CI)或 -y 自动连续执行;退出码:0=通过 1=有保留 2=不通过。

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type checkItem struct {
	status string // ok | warn | bad | info
	text   string
}

func (c checkItem) icon() string {
	switch c.status {
	case "ok":
		return "✓"
	case "warn":
		return "△"
	case "bad":
		return "✗"
	default:
		return "·"
	}
}

var inspectReader *bufio.Reader

// pause 暂停等待用户回车;返回 false 表示用户要退出
func pause(auto bool, step, total int) bool {
	if auto {
		return true
	}
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return true // 非 TTY,自动继续
	}
	if inspectReader == nil {
		inspectReader = bufio.NewReader(os.Stdin)
	}
	fmt.Printf("\n  %s [Enter]=%s / q=%s > ", fmt.Sprintf("(%d/%d)", step, total),
		T("next step"), T("quit"))
	line, _ := inspectReader.ReadString('\n')
	line = strings.TrimSpace(line)
	fmt.Println()
	return line != "q" && line != "Q" && line != "exit" && line != "quit"
}

func item(items []checkItem, status, format string, a ...any) []checkItem {
	// format 传英文常量 key,内部翻译后再格式化(与 TF 同构,vet 友好)
	return append(items, checkItem{status, fmt.Sprintf(T(format), a...)})
}

// runCmd 带超时执行外部命令,失败安静降级
func runCmd(timeout time.Duration, name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

func cpuInfoField(key string) string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, key) {
			f := strings.SplitN(line, ":", 2)
			if len(f) == 2 {
				return strings.TrimSpace(f[1])
			}
		}
	}
	return ""
}

func hasCPUFlag(flag string) bool {
	return strings.Contains(" "+cpuInfoField("flags")+" ", " "+flag+" ")
}

// localFill 本地打满全部核,返回(实际核数, 每核算力)
func localFill(ms int) (float64, float64) {
	n := numCPU()
	u0, s0, ok0 := readSelfCPUTicks()
	t0 := time.Now()
	deadline := t0.Add(time.Duration(ms) * time.Millisecond)
	var total uint64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local uint64
			for time.Now().Before(deadline) {
				burn(50000)
				local += 50000
			}
			mu.Lock()
			total += local
			mu.Unlock()
		}()
	}
	wg.Wait()
	el := time.Since(t0)
	if el.Seconds() <= 0 || !ok0 {
		return 0, 0
	}
	if u1, s1, ok := readSelfCPUTicks(); ok {
		cpuSec := float64((u1+s1)-(u0+s0)) / 100
		if cpuSec < 0 {
			cpuSec = 0
		}
		eff := cpuSec / el.Seconds()
		return eff, float64(total) / el.Seconds() / float64(n)
	}
	return 0, 0
}

// localStealJitter 独立采样窗口:空闲 steal% 与调度抖动
func localStealJitter(sec int) (stealAvg, stealMax, jitP50, jitP99, jitMax float64, over10 int) {
	var jitMu sync.Mutex
	var jits []float64
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			t0 := time.Now()
			time.Sleep(10 * time.Millisecond)
			ms := float64(time.Since(t0).Microseconds())/1000 - 10
			if ms < 0 {
				ms = 0
			}
			jitMu.Lock()
			jits = append(jits, ms)
			jitMu.Unlock()
		}
	}()
	var steals []float64
	prev, ok := readCPUTimes()
	for i := 0; i < sec; i++ {
		time.Sleep(time.Second)
		ct, ok2 := readCPUTimes()
		if ok && ok2 && ct.total() > prev.total() {
			dt := ct.total() - prev.total()
			steals = append(steals, float64(ct.steal-prev.steal)/float64(dt)*100)
		}
		prev, ok = ct, ok2
	}
	close(done)
	jitMu.Lock()
	st := statsOf(jits)
	jitMu.Unlock()
	for _, v := range jits {
		if v >= 10 {
			over10++
		}
	}
	sst := statsOf(steals)
	return sst.Avg, sst.Max, st.P50, st.P99, st.Max, over10
}

// localDiskTest 本地磁盘:顺序写+fsync 与 小文件 fsync 延迟
func localDiskTest(mb, files int) (writeMBps, fsyncMs float64, small stat, ok bool) {
	dir := os.TempDir()
	buf := randBuf(1 << 20)
	f, err := os.CreateTemp(dir, "vpsbench-insp-")
	if err != nil {
		return 0, 0, stat{}, false
	}
	name := f.Name()
	defer os.Remove(name)
	t0 := time.Now()
	for i := 0; i < mb; i++ {
		if _, err := f.Write(buf); err != nil {
			return 0, 0, stat{}, false
		}
	}
	wEl := time.Since(t0)
	t1 := time.Now()
	f.Sync()
	fsEl := time.Since(t1)
	f.Close()

	smallBuf := randBuf(4096)
	var lats []float64
	for i := 0; i < files; i++ {
		g, err := os.CreateTemp(dir, "vpsbench-insp-sf-")
		if err != nil {
			break
		}
		s := time.Now()
		g.Write(smallBuf)
		g.Sync()
		g.Close()
		os.Remove(g.Name())
		lats = append(lats, msF(time.Since(s)))
	}
	return float64(mb) / wEl.Seconds(), msF(fsEl), statsOf(lats), true
}

// ---- 各步骤 ----

func stepSpecs(cores, ram, disk int) []checkItem {
	var it []checkItem
	virt, _ := runCmd(5*time.Second, "systemd-detect-virt")
	if virt == "" {
		virt = "?"
	}
	switch virt {
	case "openvz", "lxc", "docker":
		it = item(it, "warn", "virtualization: %s (container — steal is invisible, ceiling is limited)", virt)
	default:
		it = item(it, "info", "virtualization: %s", virt)
	}
	model := cpuInfoField("model name")
	n := numCPU()
	it = item(it, "info", "CPU: %s | %d vCPU", model, n)
	if cores > 0 {
		if n == cores {
			it = item(it, "ok", "cores match order: %d = %d", cores, n)
		} else {
			it = item(it, "bad", "cores mismatch: ordered %d, got %d", cores, n)
		}
	}
	total, avail := readMemKB()
	it = item(it, "info", "memory: %d MB total, %d MB available, swap %d MB", total/1024, avail/1024, swapMB())
	if ram > 0 {
		gotF := float64(total) / 1024 / 1024 // GiB
		if gotF >= float64(ram)*0.9 && gotF <= float64(ram)*1.15 {
			it = item(it, "ok", "RAM matches order: %dG ≈ %.1fG", ram, gotF)
		} else {
			it = item(it, "bad", "RAM mismatch: ordered %dG, got %.1fG", ram, gotF)
		}
	}
	rootGB := rootFSGiB()
	it = item(it, "info", "root disk: %d GiB", rootGB)
	if disk > 0 {
		if abs64(int64(rootGB)-int64(disk)) <= int64(disk)/10+1 {
			it = item(it, "ok", "disk matches order: %dG ≈ %dG", disk, rootGB)
		} else {
			it = item(it, "bad", "disk mismatch: ordered %dG, got %dG", disk, rootGB)
		}
	}
	if sw := swapMB(); sw > 0 && int64(sw) >= int64(total)/2 {
		it = item(it, "warn", "swap ≥ half of RAM (%d MB) — some providers fake RAM with swap", sw)
	}
	osName, _ := runCmd(5*time.Second, "sh", "-c", ". /etc/os-release 2>/dev/null && echo $PRETTY_NAME")
	if osName == "" {
		osName = "?"
	}
	it = item(it, "info", "OS: %s | %s | up %s", osName, kernelVersion(),
		(time.Duration(hostUptimeSec() * float64(time.Second))).Round(time.Hour))
	return it
}

func stepCPU() []checkItem {
	var it []checkItem
	if !hasCPUFlag("aes") {
		it = item(it, "warn", "no AES-NI — crypto/VPN throughput will suffer")
	} else {
		it = item(it, "ok", "AES-NI available")
	}
	_ = hasCPUFlag("avx2")

	// 单核
	best := 0.0
	for i := 0; i < 2; i++ {
		t0 := time.Now()
		var ops uint64
		for time.Since(t0) < time.Second {
			burn(20000)
			ops += 20000
		}
		if v := float64(ops); v > best {
			best = v
		}
	}
	it = item(it, "info", "single-core: %.0f M ops/s (typical VPS: 100–400)", best/1e6)

	// 满载
	eff, perCore := localFill(5000)
	ratio := eff / float64(numCPU())
	it = item(it, "info", "full-load: %.2f/%d cores delivered (%.0f%%)", eff, numCPU(), ratio*100)
	if best > 0 && perCore > 0 {
		it = item(it, "info", "per-core under load: %.0f%% of idle", perCore/best*100)
	}
	switch {
	case ratio >= 0.95:
		it = item(it, "ok", "cores are real — no oversell/throttle")
	case ratio >= 0.85:
		it = item(it, "warn", "mild contention: %.0f%% delivered", ratio*100)
	default:
		it = item(it, "bad", "oversold/throttled: only %.0f%% of advertised cores", ratio*100)
	}
	return it
}

func stepMem() []checkItem {
	var it []checkItem
	_, avail := readMemKB()
	mb := 512
	if int64(mb) > avail/1024-256 {
		mb = int(avail/1024) - 256
	}
	if mb < 64 {
		it = item(it, "warn", "not enough free memory to test (%d MB)", avail/1024)
		return it
	}
	b := make([]byte, mb<<20)
	for i := 0; i < len(b); i += 4096 { // 预热一遍:完成物理页分配
		b[i] = 1
	}
	t0 := time.Now()
	for i := 0; i < len(b); i += 4096 { // 第二遍才是真实带宽
		b[i] = 2
	}
	el := time.Since(t0)
	gbps := float64(mb) / 1024 / el.Seconds()
	it = item(it, "info", "memory bandwidth: %d MB touched in %s → %.1f GB/s", mb, fmtMs(msF(el)), gbps)
	if gbps < 2 {
		it = item(it, "warn", "memory bandwidth is low (%.1f GB/s) — THP/swap-backed memory?", gbps)
	} else {
		it = item(it, "ok", "memory bandwidth normal")
	}
	return it
}

func stepDisk() []checkItem {
	var it []checkItem
	rot, dev := rootRotational()
	virtio := strings.HasPrefix(dev, "vd") || strings.HasPrefix(dev, "xvd") || strings.HasPrefix(dev, "nvme")
	switch {
	case rot == "0":
		it = item(it, "info", "root disk type: SSD/NVMe (non-rotational)")
	case rot == "1" && !virtio:
		it = item(it, "warn", "root disk is ROTATIONAL (HDD) — IO latency will be high")
	case rot == "1" && virtio:
		it = item(it, "info", "root disk: %s (virtual; rotational flag unreliable, judging by fsync latency below)", dev)
	default:
		it = item(it, "info", "root disk type: unknown")
	}
	wMBps, fsMs, small, ok := localDiskTest(64, 50)
	if !ok {
		it = item(it, "warn", "disk test failed (permissions?)")
		return it
	}
	it = item(it, "info", "seq write: %.0f MB/s | 64MB fsync: %s", wMBps, fmtMs(fsMs))
	it = item(it, "info", "small-file fsync p50 %s / p99 %s / max %s",
		fmtMs(small.P50), fmtMs(small.P99), fmtMs(small.Max))
	switch {
	case small.P99 < 3:
		it = item(it, "ok", "storage latency good")
	case small.P99 < 10:
		it = item(it, "warn", "storage latency mediocre — network storage or noisy neighbors")
	default:
		it = item(it, "bad", "storage latency is HIGH (p99 %s) — heavily contended IO", fmtMs(small.P99))
	}
	return it
}

func stepNetwork() []checkItem {
	var it []checkItem
	// 公网 IP 归属(best effort)
	if body, ok := httpGetStr(5, "https://ipinfo.io/json"); ok {
		var ip struct {
			IP      string `json:"ip"`
			City    string `json:"city"`
			Country string `json:"country"`
			Org     string `json:"org"`
		}
		if json.Unmarshal([]byte(body), &ip) == nil && ip.IP != "" {
			it = item(it, "info", "public IP: %s (%s, %s) %s", ip.IP, ip.City, ip.Country, ip.Org)
		}
	} else {
		it = item(it, "info", "public IP lookup skipped (no outbound https?)")
	}
	// IPv6
	if hasGlobalIPv6() {
		it = item(it, "ok", "IPv6 available")
	} else {
		it = item(it, "info", "no global IPv6")
	}
	// DNS
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(ctx, "github.com"); err != nil {
		it = item(it, "bad", "DNS resolution failed: %v", err)
	} else {
		it = item(it, "ok", "DNS resolution ok")
	}
	// 下行带宽(cloudflare 测速点,best effort)
	if _, err := exec.LookPath("curl"); err == nil {
		if out, ok := runCmd(15*time.Second, "curl", "-s", "-o", "/dev/null",
			"-w", "%{speed_download}", "-m", "12",
			"https://speed.cloudflare.com/__down?bytes=52428800"); ok {
			if v, err := strconv.ParseFloat(strings.TrimSpace(out), 64); err == nil && v > 0 {
				mbps := v / 1e6
				it = item(it, "info", "public downlink: %.1f MB/s ≈ %.0f Mbps (cloudflare)", mbps, mbps*8)
				if mbps < 5 {
					it = item(it, "warn", "public downlink is slow (<40 Mbps)")
				}
			}
		}
	}
	// 回环 HTTP 吞吐(协议栈健康度)
	if rps := loopbackRPS(3 * time.Second); rps > 0 {
		it = item(it, "info", "loopback HTTP: %.0f req/s @16 conns (local stack)", rps)
		if rps < 2000 {
			it = item(it, "warn", "loopback RPS is low — check CPU/governor")
		}
	}
	return it
}

func stepOversell() []checkItem {
	var it []checkItem
	fmt.Printf("  %s %ds...\n", T("idle sampling"), 15)
	stealAvg, stealMax, jitP50, jitP99, jitMax, over10 := localStealJitter(15)
	it = item(it, "info", "steal%%: avg %.1f | peak %.1f", stealAvg, stealMax)
	it = item(it, "info", "jitter (10ms base): p50 %s | p99 %s | peak %s | >10ms ×%d",
		fmtMs(jitP50), fmtMs(jitP99), fmtMs(jitMax), over10)
	bad := false
	switch {
	case stealAvg < 1 && stealMax < 10 && jitP99 < 10:
		it = item(it, "ok", "no steal while idle — host is not crowded")
	case stealAvg < 3 && stealMax < 25 && jitP99 < 30:
		it = item(it, "warn", "mild steal/contention detected")
		bad = true
	default:
		it = item(it, "bad", "obvious steal — heavily oversold host")
		bad = true
	}
	if !bad {
		it = item(it, "info", "tip: 15s is a snapshot — run `vpsbench steal -w` for 24h to catch peaks")
	}
	return it
}

func stepSecurity() []checkItem {
	var it []checkItem
	// sshd
	if out, ok := runCmd(5*time.Second, "sshd", "-T"); ok {
		get := func(k string) string {
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, k+" ") {
					return strings.Fields(line)[1]
				}
			}
			return ""
		}
		rootLogin, pwdAuth := get("permitrootlogin"), get("passwordauthentication")
		it = item(it, "info", "sshd: PermitRootLogin=%s PasswordAuthentication=%s", rootLogin, pwdAuth)
		if rootLogin == "yes" && pwdAuth == "yes" {
			it = item(it, "warn", "root + password login both enabled — brute-forceable; use keys or disable one")
		}
	} else {
		it = item(it, "info", "sshd: sshd -T %s", T("not available (not root?)"))
	}
	// uid=0 用户
	if b, err := os.ReadFile("/etc/passwd"); err == nil {
		n := 0
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Split(line, ":")
			if len(f) > 2 && f[2] == "0" && f[0] != "root" {
				n++
			}
		}
		if n > 0 {
			it = item(it, "bad", "%d non-root users with UID 0!", n)
		} else {
			it = item(it, "ok", "no unexpected UID-0 users")
		}
	}
	// 爆破痕迹
	if out, ok := runCmd(5*time.Second, "sh", "-c", "lastb 2>/dev/null | head -5000 | wc -l"); ok {
		if n, _ := strconv.Atoi(strings.TrimSpace(out)); n > 0 {
			it = item(it, "info", "%d failed-login records (lastb) — internet noise; install fail2ban", n)
		}
	}
	// 监听端口
	if out, ok := runCmd(5*time.Second, "ss", "-tlnp"); ok {
		lines := strings.Split(out, "\n")
		n := 0
		for _, l := range lines[1:] {
			if strings.TrimSpace(l) != "" {
				n++
			}
		}
		it = item(it, "info", "%d listening TCP ports", n)
	}
	// 防火墙
	if _, ok := runCmd(5*time.Second, "ufw", "status"); ok {
		if out, ok := runCmd(5*time.Second, "ufw", "status"); ok {
			if strings.HasPrefix(out, "Status: active") {
				it = item(it, "ok", "firewall: ufw active")
			} else {
				it = item(it, "warn", "firewall: ufw INACTIVE — exposure risk")
			}
		}
	} else if _, ok := runCmd(5*time.Second, "iptables", "-L", "-n"); ok {
		it = item(it, "info", "firewall: iptables present (rules not verified)")
	} else {
		it = item(it, "warn", "no firewall tooling found (ufw/iptables)")
	}
	// 安全更新
	if _, err := exec.LookPath("apt-get"); err == nil {
		if out, ok := runCmd(25*time.Second, "sh", "-c", "apt-get -s upgrade 2>/dev/null | grep -c '^Inst' || true"); ok {
			if n, _ := strconv.Atoi(strings.TrimSpace(out)); n > 0 {
				it = item(it, "warn", "%d packages upgradable — patch soon (apt upgrade)", n)
			} else {
				it = item(it, "ok", "all packages up to date")
			}
		}
	}
	return it
}

// ---- 工具 ----

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func swapMB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "SwapTotal:") {
			f := strings.Fields(line)
			if len(f) > 1 {
				v, _ := strconv.ParseInt(f[1], 10, 64)
				return v / 1024
			}
		}
	}
	return 0
}

func rootFSGiB() int64 {
	if out, ok := runCmd(5*time.Second, "df", "-BG", "--output=size", "/"); ok {
		f := strings.Fields(out)
		if len(f) >= 1 {
			s := strings.TrimSuffix(f[len(f)-1], "G")
			v, _ := strconv.ParseInt(s, 10, 64)
			return v
		}
	}
	return 0
}

func rootRotational() (rot, dev string) {
	if out, ok := runCmd(5*time.Second, "df", "--output=source", "/"); ok {
		f := strings.Fields(out)
		if len(f) >= 2 {
			dev = strings.TrimPrefix(f[len(f)-1], "/dev/")
			dev = regexp.MustCompile(`\d+$`).ReplaceAllString(dev, "")
			if b, err := os.ReadFile("/sys/block/" + dev + "/queue/rotational"); err == nil {
				return strings.TrimSpace(string(b)), dev
			}
		}
	}
	return "", dev
}

func hasGlobalIPv6() bool {
	b, err := os.ReadFile("/proc/net/if_inet6")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && !strings.HasPrefix(f[0], "fe80") && !strings.HasPrefix(f[0], "::1") {
			return true
		}
	}
	return false
}

func httpGetStr(timeoutSec int, url string) (string, bool) {
	cl := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// loopbackRPS 起临时 gin 服务压回环,评估本机协议栈/调度健康度
func loopbackRPS(d time.Duration) float64 {
	srv := &http.Server{Handler: newRouter(), ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	go srv.Serve(ln)
	defer srv.Close()
	base := "http://" + ln.Addr().String()
	res := testLoad(base, 16, d, 0)
	if res.Elapsed.Seconds() <= 0 {
		return 0
	}
	return float64(res.Total) / res.Elapsed.Seconds()
}

// ---- 主流程 ----

func runInspectCmd(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	auto := fs.Bool("y", false, T("auto-continue, no pauses (for scripts/CI)"))
	cores := fs.Int("cores", 0, T("ordered CPU cores, e.g. 2 (0=skip check)"))
	ram := fs.Int("ram", 0, T("ordered RAM GB, e.g. 4 (0=skip)"))
	disk := fs.Int("disk", 0, T("ordered disk GB, e.g. 80 (0=skip)"))
	save := fs.String("o", "", T("report file (default ~/vpsbench-inspect-<host>-<date>.txt, empty=auto)"))
	fs.Parse(reorderArgs(fs, args))

	type step struct {
		name string
		fn   func() []checkItem
	}
	steps := []step{
		{T("Step 1 · Specs & order match"), func() []checkItem { return stepSpecs(*cores, *ram, *disk) }},
		{T("Step 2 · CPU reality & oversell"), stepCPU},
		{T("Step 3 · Memory"), stepMem},
		{T("Step 4 · Disk & IO"), stepDisk},
		{T("Step 5 · Network"), stepNetwork},
		{T("Step 6 · Idle steal (the core check)"), stepOversell},
		{T("Step 7 · Security baseline"), stepSecurity},
	}
	host, _ := os.Hostname()
	fmt.Println()
	fmt.Println(strings.Repeat("─", 56))
	fmt.Println("  " + T("vpsbench one-click acceptance inspection") + " · " + host)
	fmt.Println(strings.Repeat("─", 56))

	var all []checkItem
	var stepItems [][]checkItem
	for i, st := range steps {
		fmt.Printf("\n%s\n  %s\n%s\n", strings.Repeat("─", 56), st.name, strings.Repeat("─", 56))
		items := st.fn()
		for _, c := range items {
			fmt.Printf("  %s %s\n", c.icon(), c.text)
		}
		all = append(all, items...)
		stepItems = append(stepItems, items)
		if !pause(*auto, i+1, len(steps)+1) {
			fmt.Println("  " + T("inspection aborted"))
			os.Exit(1)
		}
	}

	// 验收单
	okN, warnN, badN := 0, 0, 0
	for _, c := range all {
		switch c.status {
		case "ok":
			okN++
		case "warn":
			warnN++
		case "bad":
			badN++
		}
	}
	code := 0
	concl := T("ACCEPTED — good to go")
	if badN > 0 {
		code = 2
		concl = T("REJECTED — open a ticket / refund")
	} else if warnN > 0 {
		code = 1
		concl = T("ACCEPTED WITH NOTES — %d warnings, fix when convenient")
	}
	fmt.Printf("\n%s\n  %s\n%s\n", strings.Repeat("═", 56), T("Acceptance summary"), strings.Repeat("═", 56))
	fmt.Printf("  ✓ %d   △ %d   ✗ %d\n", okN, warnN, badN)
	verdictLine := concl
	if warnN > 0 && badN == 0 {
		verdictLine = fmt.Sprintf(T("ACCEPTED WITH NOTES — %d warnings, fix when convenient"), warnN)
	}
	fmt.Println("  " + T("Verdict") + ": " + verdictLine)

	if *save == "" {
		home := os.Getenv("HOME")
		if home == "" {
			home = "/root"
		}
		*save = filepath.Join(home, fmt.Sprintf("vpsbench-inspect-%s-%s.txt", host, time.Now().Format("20060102-1504")))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "vpsbench inspect · %s · %s\n%s\n", host, time.Now().Format("2006-01-02 15:04:05"), strings.Repeat("─", 56))
	for i, st := range steps {
		fmt.Fprintf(&b, "\n%s\n", st.name)
		if i < len(stepItems) {
			for _, c := range stepItems[i] {
				fmt.Fprintf(&b, "  %s %s\n", c.icon(), c.text)
			}
		}
	}
	fmt.Fprintf(&b, "\n%s\n  ✓ %d  △ %d  ✗ %d  |  %s\n", strings.Repeat("═", 56), okN, warnN, badN, verdictLine)
	if os.WriteFile(*save, []byte(b.String()), 0644) == nil {
		fmt.Println("  " + T("report saved") + ": " + *save)
	}
	os.Exit(code)
}
