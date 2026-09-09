# vpsbench — VPS performance / concurrency / oversell (shared-host steal) tester

[English](README.md) | [简体中文](README.zh-CN.md)

Single static binary. Two ways to use, same API:

1. **Gin server + embedded web UI** — run on the VPS under test, open in browser, click buttons.
2. **CLI client** — drive the same API from LAN / headless machines. **No server needed**: omit `<url>` and every command self-tests the local machine via an in-process server.

English by default (mojibake-safe); add `-lang zh` (or env `VPSBENCH_LANG=zh`) for Chinese.

## One-click acceptance inspection (new VPS)

```bash
vpsbench inspect -cores 2 -ram 4 -disk 80 -lang zh   # 8-step wizard, Enter through each step
```

7 checks + acceptance sheet: specs vs your order, CPU reality & oversell, memory, disk/IO,
network (public IP/geo, IPv6, DNS, downlink, loopback RPS), **idle steal & jitter**,
security baseline (sshd, UID-0 users, brute-force records, firewall, pending updates).
Exit code 0/1/2 = accepted / with notes / rejected. Report saved to `~/vpsbench-inspect-*.txt`.

## One-click install (curl from GitHub Releases)

```bash
curl -fsSL https://raw.githubusercontent.com/ankye/vpsbench/main/install.sh | sh
```

The script auto-detects your OS/architecture (linux/darwin × amd64/arm64/386/arm),
downloads the matching static binary from the latest
[Release](https://github.com/ankye/vpsbench/releases) and verifies its SHA-256.
Pin a version with `VPSBENCH_VERSION=v1.0.2`.

Prefer a direct download? Pick your platform:

```bash
# linux/amd64 example
curl -fsSL -o vpsbench https://github.com/ankye/vpsbench/releases/latest/download/vpsbench-linux-amd64
chmod +x vpsbench
```

| Platform | Asset |
|---|---|
| Linux x86_64 | `vpsbench-linux-amd64` |
| Linux ARM64 | `vpsbench-linux-arm64` |
| Linux 386 / ARMv7 | `vpsbench-linux-386` / `vpsbench-linux-arm` |
| macOS Intel / Apple Silicon | `vpsbench-darwin-amd64` / `vpsbench-darwin-arm64` |
| Windows x64 / ARM64 | `vpsbench-windows-amd64.exe` / `vpsbench-windows-arm64.exe` |

## Quick start

```bash
# On the VPS under test
vpsbench server -addr :8300 -token YOUR_SECRET
ufw allow 8300/tcp

# Option A: browser → http://<vps-ip>:8300  (token box top-right, EN/中文 toggle), click to test
# Option B: CLI from anywhere
vpsbench all http://<vps-ip>:8300
vpsbench steal http://<vps-ip>:8300 -w          # keep watching 24h for peak-hour steal
```

## What it measures

| Command / button | Measures |
|---|---|
| `inspect` | **One-click acceptance inspection**: 8-step wizard (specs vs order, CPU oversell, memory, disk, network, idle steal, security baseline) + acceptance sheet, exit code 0/1/2 |
| `ping` | RTT percentiles + jitter, catches steal-induced latency spikes |
| `load -c 50 [-size N]` | HTTP concurrency: RPS / error rate / p99 (size = response bytes) |
| `packet -c 100` | 1K/2K/4K packet-size ladder: RPS / throughput / percentiles + bottleneck analysis |
| `conn -c 500` | Max concurrent TCP connections (browsers cap ~6 — use CLI) |
| `bw [-up]` | Download / upload bandwidth |
| `cpu` | Single-core speed (M ops/s, comparable across machines) |
| `fill -n 0` | Fill all cores → **core delivery ratio** (oversell / cgroup-quota detection) |
| `disk` | Seq R/W bandwidth + small-file fsync latency (most I/O-steal-sensitive metric) |
| `diag -d 10` | **One-click diagnosis**: which of CPU/GPU/IO/socket/mem/disk loads highest, p50/p95/p99, ideal-cap table, % of ideal |
| `steal [-w]` | **CPU steal% + scheduling jitter report** — the core shared-host steal metrics |
| `all` | Full suite + copy-paste All-in-One server summary |

## Reading the steal metrics

- **steal%** — CPU time the hypervisor admits giving to other tenants (`/proc/stat`).
  Idle-time steal consistently > 2% or frequent red events ⇒ oversold host.
- **scheduling jitter** — actual-vs-expected delay of a 10ms sleep. When vCPUs get parked
  you'll see tens-to-hundreds of ms spikes — that *is* the latency of being stolen.
  Idle-time p99 > 20 ms is abnormal.
- **core delivery** — CPU-seconds actually received ÷ wall time with all cores busy.
  < 85% ⇒ advertised cores inflated or throttled.
- **small-file fsync p99** — > 10 ms ⇒ storage fought over by neighbors.
- steal stuck at 0 but jitter high ⇒ possibly OpenVZ/LXC (steal invisible there) — trust the jitter.

## Build from source

```bash
go build -o vpsbench .
```

Releases are built by **GitHub Actions**: push a `v*` tag (e.g. `git tag v1.0.1 && git push origin v1.0.1`)
and the [release workflow](.github/workflows/release.yml) cross-compiles all platforms,
attaches binaries + `SHA256SUMS` to the release automatically.
