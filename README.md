# gomon

A single-binary resource monitor for tiny Linux boxes, written to run on a
**JZ01-45-V33 USB 4G modem stick** (Qualcomm MSM8916 / Snapdragon 410, 4 cores
@ 800 MHz, **376 MB RAM**, 3.96 GB eMMC) running Debian 13 with a
`6.12.1-msm8916` kernel.

It exists because nginx + PHP + a database is a lot of machine for a board with
376 MB of RAM. `gomon` is one **5.2 MB static binary** with the dashboard
embedded in it, and it sits at about **9.5 MB RSS**. No web server, no runtime,
no database, no cgo, and **no third-party Go modules** — everything comes from
the standard library.

![gomon dashboard](Home.png)

---

## Install — prebuilt

Nothing to build and nothing to install alongside it. On the device:

```bash
curl -fsSL https://github.com/khamdan/gomon/releases/latest/download/gomon-linux-arm64.tar.gz | tar xz
sudo gomon-linux-arm64/install.sh
```

That drops the binary and `gomonctl` in `/usr/local/bin`, installs the systemd
unit, **enables it at boot and starts it now**, then prints the URL to open —
`http://<device-ip>:8080`.

Use `gomon-linux-amd64.tar.gz` on a normal PC. To remove it again:

```bash
sudo gomon-linux-arm64/install.sh uninstall
```

The service runs as `nobody` with `ProtectSystem=strict`, `NoNewPrivileges`,
`RestrictAddressFamilies=AF_INET AF_INET6` and `MemoryMax=64M`. It needs no
privileges — everything it reads is world-readable and `statfs` needs no rights.

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

### HTTP endpoints

| path | returns |
|---|---|
| `/` | the dashboard (embedded HTML) |
| `/api/stats` | current snapshot, JSON |
| `/api/history` | the last 180 samples, JSON |

### Flags

```
-listen :8080      address to listen on
-disks /,/boot     comma-separated mount points to report
```

---

## Build it yourself

Go 1.21+. There is nothing to fetch.

```bash
go build -o gomon .
```

On the stick itself this takes about 2m40s and pushes ~41 MB into zram swap, so
**cross-compiling from a workstation is the nicer route**:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o gomon .
scp gomon gomon.service gomonctl install.sh user@<device>:/tmp/gomon-dist/
ssh user@<device> 'sudo /tmp/gomon-dist/install.sh'
```

`install.sh` installs whatever sits next to it, so it works the same from a
release tarball or from your own build.

### Releasing

`.github/workflows/release.yml` builds both architectures and publishes the
tarballs. Push a tag:

```bash
git tag v1.0.0 && git push --tags
```

Asset names carry no version (`gomon-linux-arm64.tar.gz`), which is what keeps
the `releases/latest/download/` URL above working forever.

---

## gomonctl

A thin wrapper over `systemctl` so you don't have to remember the unit name:

```
gomonctl start | stop | restart | status | enable | disable | logs | rebuild
```

`enable` turns on start-at-boot *and* starts it now; `disable` mirrors that.
`status` prints the URL someone on the LAN would actually type, derived from
`ip -4 -o addr`. Rather than sleeping a fixed amount after starting, it polls
`/api/stats` until the server answers — the board is slow enough that a flat
`sleep 1` is wrong in both directions. `rebuild` expects the source in `~/gomon`
and is the only verb that does nothing on a binary-only install.

---

## Portability

The metrics are ordinary Linux `/proc` and `/sys` reads, so this runs on any
Linux box. The only board-specific thing left in here is the default
`-disks /,/boot`, which assumes `/boot` is its own partition — it is on this
stick (`mmcblk0p13`).
