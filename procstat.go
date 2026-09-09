package main

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// cpuTimes 对应 /proc/stat 第一行 "cpu" 的各列(单位: ticks,通常 100 ticks = 1s)
type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (c cpuTimes) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

func readCPUTimes() (cpuTimes, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		f := strings.Fields(line)[1:]
		vals := make([]uint64, len(f))
		for i, s := range f {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			vals[i] = v
		}
		get := func(i int) uint64 {
			if i < len(vals) {
				return vals[i]
			}
			return 0
		}
		return cpuTimes{
			user: get(0), nice: get(1), system: get(2), idle: get(3),
			iowait: get(4), irq: get(5), softirq: get(6), steal: get(7),
		}, true
	}
	return cpuTimes{}, false
}

// readSelfCPUTicks 读取本进程的 utime/stime(/proc/self/stat 第 14/15 列,单位 ticks)
func readSelfCPUTicks() (uint64, uint64, bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, false
	}
	// comm 字段可能含空格,从最后一个 ')' 之后切
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 > len(b) {
		return 0, 0, false
	}
	fields := strings.Fields(string(b[i+2:]))
	if len(fields) < 13 { // fields[0]=state(第3列) ... fields[11]=utime(第14列)
		return 0, 0, false
	}
	u, err1 := strconv.ParseUint(fields[11], 10, 64)
	s, err2 := strconv.ParseUint(fields[12], 10, 64)
	return u, s, err1 == nil && err2 == nil
}

func readLoadavg() (float64, float64, float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0
	}
	a1, _ := strconv.ParseFloat(f[0], 64)
	a5, _ := strconv.ParseFloat(f[1], 64)
	a15, _ := strconv.ParseFloat(f[2], 64)
	return a1, a5, a15
}

func readMemKB() (total, avail int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	return
}

func kernelVersion() string {
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(b))
}

func hostUptimeSec() float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
