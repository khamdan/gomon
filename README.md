# gomon

A single-binary resource monitor for tiny Linux boxes, written to run on a
**JZ01-45-V33 USB 4G modem stick** (Qualcomm MSM8916 / Snapdragon 410, 4 cores
@ 800 MHz, **376 MB RAM**, 3.96 GB eMMC) running Debian 13 with a
`6.12.1-msm8916` kernel.

It exists because nginx + PHP + a database is a lot of machine for a board with
376 MB of RAM. `gomon` is one **5.4 MB static binary** with the dashboard
embedded in it, and it sits at about **9.5 MB RSS**. No web server, no runtime,
no database, no cgo, and **no third-party Go modules** — everything comes from
the standard library.

![endpoints: / /api/stats /api/history](https://img.shields.io/badge/deps-stdlib%20only-blue)

---

## What it shows

Everything is read straight from `/proc` and `/sys`, sampled every 2 seconds,
with 6 minutes of history kept in memory (180 points).

- **CPU** — total plus per-core utilisation, and each core's current MHz from
  `cpufreq/scaling_cur_freq`
- **Memory** — used / available / cache, plus swap
- **Load average**
- **Network** — per-interface rx/tx rates from `/proc/net/dev`
- **Temperature** — every zone under `/sys/class/thermal`
- **Storage** — usage per mount point via `statfs`
- **Top processes** — the 6 heaviest by CPU, from `/proc/[pid]/stat`
- **Host info** — model, kernel, uptime

The UI is Bootstrap 5.3 + Chart.js 4.4 pulled from jsDelivr, dark theme, served
from a single `//go:embed`ed `index.html`. Charts are backfilled from
`/api/history` on load, so a fresh page isn't blank. Timestamps are sent as Unix
milliseconds and rendered in the **viewer's** timezone, not the server's.

## HTTP endpoints

| path | returns |
|---|---|
| `/` | the dashboard (embedded HTML) |
| `/api/stats` | current snapshot, JSON |
| `/api/history` | the last 180 samples, JSON |

## Flags

```
-listen :8080      address to listen on
-disks /,/boot     comma-separated mount points to report
```

---

## Build

Go 1.21+. There is nothing to fetch.

```bash
go build -o gomon .
```

On the stick itself this takes a while and pushes ~41 MB into zram swap, so
**cross-compiling from a workstation is the nicer route**:

```bash
GOOS=linux GOARCH=arm64 go build -ldflags='-s -w' -o gomon-arm64 .
scp gomon-arm64 user@<stick>:/tmp/gomon
```

## Install

```bash
sudo install -m755 gomon      /usr/local/bin/gomon
sudo install -m755 gomonctl   /usr/local/bin/gomonctl
sudo install -m644 gomon.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo gomonctl enable          # enable at boot AND start now
```

Then browse to `http://<stick-ip>:8080`.

The unit runs as `nobody` with `ProtectSystem=strict`, `NoNewPrivileges`,
`RestrictAddressFamilies=AF_INET AF_INET6` and `MemoryMax=64M`. It needs no
privileges — everything it reads is world-readable and `statfs` needs no rights.

### gomonctl

A thin wrapper over `systemctl` so you don't have to remember the unit name:

```
gomonctl start | stop | restart | status | enable | disable | logs | rebuild
```

`enable` turns on start-at-boot *and* starts it now; `disable` mirrors that.
`status` prints the URL someone on the LAN would actually type, derived from
`ip -4 -o addr`. Rather than sleeping a fixed amount after starting, it polls
`/api/stats` until the server answers — the board is slow enough that a flat
`sleep 1` is wrong in both directions.

---

## Portability

The metrics are ordinary Linux `/proc` and `/sys` reads, so this runs on any
Linux box. The only board-specific thing left in here is the default
`-disks /,/boot`, which assumes `/boot` is its own partition — it is on this
stick (`mmcblk0p13`).
