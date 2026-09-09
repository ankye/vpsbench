package main

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// 监控器:CPU steal% 采样 + 调度抖动测量(共享宿主机"偷取"检测核心)
//
// 调度抖动原理:让一个 goroutine 周期性 sleep 10ms,测量实际耗时与预期的偏差。
// 空闲机器上偏差通常 < 2ms;若宿主机把 CPU 分片偷给了邻居,本 VM 的 vCPU
// 会被挂起,sleep 醒来/调度都会被推迟 —— 偏差直接体现为几十~几百毫秒的尖刺。
const (
	jitInterval = 10 * time.Millisecond // 抖动测量基准周期
	maxSamples  = 7200                  // 每秒采样环形保留 2 小时
	maxSpikes   = 500                   // 事件上限
	jitRingSize = 65536                 // 原始抖动值环形缓存(约 11 分钟 @10ms)
	stealEvtThr = 5.0                   // steal% 超过此值记事件
	jitEvtThr   = 50.0                  // 单次抖动超过此 ms 记事件
)

type SecSample struct {
	T           time.Time `json:"t"`
	StealPct    float64   `json:"steal_pct"`     // 本秒 CPU steal 百分比
	SelfCPUPct  float64   `json:"self_cpu_pct"`  // 本进程本秒 CPU 占用(压测时区分自家负载)
	Load1       float64   `json:"load1"`         // 1 分钟负载
	JitterMaxMs float64   `json:"jitter_max_ms"` // 本秒最大调度抖动
	JitOver10   int       `json:"jit_over_10ms"` // 本秒 >10ms 次数
	JitOver50   int       `json:"jit_over_50ms"` // 本秒 >50ms 次数
}

type SpikeEvent struct {
	T        time.Time `json:"t"`
	Kind     string    `json:"kind"` // "steal" | "jitter"
	StealPct float64   `json:"steal_pct,omitempty"`
	JitterMs float64   `json:"jitter_ms,omitempty"`
}

type StatsSnapshot struct {
	StartedAt    time.Time    `json:"started_at"`
	UptimeSec    float64      `json:"uptime_sec"`
	Hostname     string       `json:"hostname"`
	Kernel       string       `json:"kernel"`
	NumCPU       int          `json:"num_cpu"`
	SampleCount  int          `json:"sample_count"`
	StealCur     float64      `json:"steal_cur"`
	StealAvg     float64      `json:"steal_avg"`
	StealP95     float64      `json:"steal_p95"`
	StealMax     float64      `json:"steal_max"`
	JitInterval  int          `json:"jit_interval_ms"`
	JitP50       float64      `json:"jit_p50_ms"`
	JitP95       float64      `json:"jit_p95_ms"`
	JitP99       float64      `json:"jit_p99_ms"`
	JitMax       float64      `json:"jit_max_ms"`
	JitOver10    uint64       `json:"jit_over_10ms"`
	JitOver50    uint64       `json:"jit_over_50ms"`
	JitOver100   uint64       `json:"jit_over_100ms"`
	Load         [3]float64   `json:"load"`
	MemTotalMB   int64        `json:"mem_total_mb"`
	MemAvailMB   int64        `json:"mem_avail_mb"`
	Spikes       []SpikeEvent `json:"spikes,omitempty"`
	Samples      []SecSample  `json:"samples,omitempty"` // ?full=1 时返回全部
}

type Monitor struct {
	started time.Time

	mu      sync.Mutex
	samples []SecSample
	spikes  []SpikeEvent
	jitRing []float64
	jitIdx  int
	jitCnt  uint64
	over10  uint64
	over50  uint64
	over100 uint64

	winMax    float64 // 当前 1s 窗口内最大抖动
	winOver10 int
	winOver50 int

	cpuPrev  *cpuTimes
	selfPrev uint64
	selfInit bool
	lastEvt  map[string]time.Time
}

func NewMonitor() *Monitor {
	return &Monitor{started: time.Now(), lastEvt: make(map[string]time.Time)}
}

func (m *Monitor) Start() {
	go m.jitterLoop()
	go m.sampleLoop()
}

func (m *Monitor) jitterLoop() {
	for {
		t0 := time.Now()
		time.Sleep(jitInterval)
		excess := time.Since(t0) - jitInterval
		ms := float64(excess.Microseconds()) / 1000
		if ms < 0 {
			ms = 0
		}
		m.mu.Lock()
		m.jitCnt++
		switch { // >=100 计入全部三档,>=50 计入两档,>=10 计入一档
		case ms >= 100:
			m.over100++
			fallthrough
		case ms >= 50:
			m.over50++
			fallthrough
		case ms >= 10:
			m.over10++
		}
		if ms > m.winMax {
			m.winMax = ms
		}
		if ms >= 10 {
			m.winOver10++
		}
		if ms >= 50 {
			m.winOver50++
		}
		if len(m.jitRing) < jitRingSize {
			m.jitRing = append(m.jitRing, ms)
		} else {
			m.jitRing[m.jitIdx] = ms
			m.jitIdx = (m.jitIdx + 1) % jitRingSize
		}
		m.mu.Unlock()
	}
}

func (m *Monitor) sampleLoop() {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for range tk.C {
		m.takeSample()
	}
}

func (m *Monitor) takeSample() {
	now := time.Now()
	ct, ctOK := readCPUTimes()
	u, s, selfOK := readSelfCPUTicks()
	l1, _, _ := readLoadavg()

	m.mu.Lock()
	defer m.mu.Unlock()

	var stealPct, selfPct float64
	if ctOK && m.cpuPrev != nil {
		dt := ct.total() - m.cpuPrev.total()
		if dt > 0 {
			stealPct = float64(ct.steal-m.cpuPrev.steal) / float64(dt) * 100
		}
	}
	if selfOK && m.selfInit {
		// 100 ticks = 1 CPU 秒,窗口 1s ⇒ 百分比 == ticks 差值
		d := (u + s) - m.selfPrev
		if int64(d) < 0 {
			d = 0
		}
		selfPct = float64(d)
	}

	jitMax := m.winMax
	smp := SecSample{
		T: now, StealPct: r2(stealPct), SelfCPUPct: r2(selfPct), Load1: r2(l1),
		JitterMaxMs: r2(jitMax), JitOver10: m.winOver10, JitOver50: m.winOver50,
	}
	m.winMax, m.winOver10, m.winOver50 = 0, 0, 0

	m.samples = append(m.samples, smp)
	if len(m.samples) > maxSamples {
		m.samples = m.samples[len(m.samples)-maxSamples:]
	}

	// 事件记录(每类 5 秒限频,防止刷屏)
	if stealPct >= stealEvtThr && now.Sub(m.lastEvt["steal"]) > 5*time.Second {
		m.lastEvt["steal"] = now
		m.pushEvent(SpikeEvent{T: now, Kind: "steal", StealPct: r2(stealPct)})
		fmt.Printf("[%s] ⚠ "+T("CPU steal %.1f%% detected (host gave CPU to neighbors)")+"\n", now.Format("15:04:05"), stealPct)
	}
	if jitMax >= jitEvtThr && now.Sub(m.lastEvt["jitter"]) > 5*time.Second {
		m.lastEvt["jitter"] = now
		m.pushEvent(SpikeEvent{T: now, Kind: "jitter", JitterMs: r2(jitMax)})
		fmt.Printf("[%s] ⚠ "+T("scheduling jitter spike %.1fms (base 10ms, likely steal/contention)")+"\n", now.Format("15:04:05"), jitMax)
	}

	if ctOK {
		c := ct
		m.cpuPrev = &c
	}
	if selfOK {
		m.selfPrev = u + s
		m.selfInit = true
	}
}

func (m *Monitor) pushEvent(e SpikeEvent) {
	m.spikes = append(m.spikes, e)
	if len(m.spikes) > maxSpikes {
		m.spikes = m.spikes[len(m.spikes)-maxSpikes:]
	}
}

func (m *Monitor) Snapshot(full bool) StatsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	total, avail := readMemKB()
	l1, l5, l15 := readLoadavg()
	host, _ := osHostname()
	nc := numCPU()

	snap := StatsSnapshot{
		StartedAt: m.started, UptimeSec: time.Since(m.started).Seconds(),
		Hostname: host, Kernel: kernelVersion(), NumCPU: nc,
		SampleCount: len(m.samples),
		JitInterval: int(jitInterval.Milliseconds()),
		JitOver10:   m.over10, JitOver50: m.over50, JitOver100: m.over100,
		Load:       [3]float64{l1, l5, l15},
		MemTotalMB: total / 1024, MemAvailMB: avail / 1024,
	}

	if n := len(m.samples); n > 0 {
		var steals []float64
		for _, s := range m.samples {
			steals = append(steals, s.StealPct)
		}
		st := statsOf(steals)
		snap.StealCur = m.samples[n-1].StealPct
		snap.StealAvg = r2(st.Avg)
		snap.StealP95 = r2(st.P95)
		snap.StealMax = r2(st.Max)
	}
	if len(m.jitRing) > 0 {
		st := statsOf(m.jitRing)
		snap.JitP50 = r2(st.P50)
		snap.JitP95 = r2(st.P95)
		snap.JitP99 = r2(st.P99)
		snap.JitMax = r2(st.Max)
	}
	if len(m.spikes) > 0 {
		snap.Spikes = append([]SpikeEvent(nil), m.spikes...)
	}
	if full && len(m.samples) > 0 {
		snap.Samples = append([]SecSample(nil), m.samples...)
	}
	return snap
}

// jitterSorted 供测试用
func jitterSorted(in []float64) []float64 {
	out := append([]float64(nil), in...)
	sort.Float64s(out)
	return out
}

func r2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
