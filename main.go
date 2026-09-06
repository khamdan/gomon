// gomon - a small resource monitor for the JZ01-45-V33 4G modem stick.
//
// Single static binary, stdlib only. Reads everything from /proc and /sys,
// keeps a short in-memory history, and serves a Bootstrap 5 + Chart.js page.
//
//	/            dashboard
//	/api/stats   current snapshot (JSON)
//	/api/history backfill for the charts (JSON)
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "embed"
)

//go:embed index.html
var indexHTML []byte

const (
	interval   = 2 * time.Second
	historyLen = 180 // 180 * 2s = 6 minutes
	topProcs   = 6
)

// ---------------------------------------------------------------- data types

// Point is one history sample. Kept deliberately small: the whole history is
// sent to the browser on page load.
type Point struct {
	T    int64   `json:"t"` // unix millis
	CPU  float64 `json:"cpu"`
	Mem  float64 `json:"mem"`
	Rx   float64 `json:"rx"` // bytes/sec
	Tx   float64 `json:"tx"`
	Temp float64 `json:"temp"` // hottest zone, °C
}

type Core struct {
	Name string  `json:"name"`
	Pct  float64 `json:"pct"`
	MHz  int     `json:"mhz"`
}

type Temp struct {
	Name string  `json:"name"`
	C    float64 `json:"c"`
}

type Disk struct {
	Path    string  `json:"path"`
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	UsedPct float64 `json:"usedPct"`
}

type Iface struct {
	Name    string  `json:"name"`
	Rx      float64 `json:"rx"` // bytes/sec
	Tx      float64 `json:"tx"`
	RxTotal uint64  `json:"rxTotal"`
	TxTotal uint64  `json:"txTotal"`
}

type Proc struct {
	PID  int     `json:"pid"`
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"`
	RSS  uint64  `json:"rss"`
}

type Snapshot struct {
	Time      int64     `json:"time"`
	Host      string    `json:"host"`
	Model     string    `json:"model"`
	Kernel    string    `json:"kernel"`
	Uptime    float64   `json:"uptime"`
	CPU       float64   `json:"cpu"`
	Cores     []Core    `json:"cores"`
	Load      []float64 `json:"load"`
	MemTotal  uint64    `json:"memTotal"`
	MemUsed   uint64    `json:"memUsed"`
	MemAvail  uint64    `json:"memAvail"`
	MemCache  uint64    `json:"memCache"`
	MemPct    float64   `json:"memPct"`
	SwapTotal uint64    `json:"swapTotal"`
	SwapUsed  uint64    `json:"swapUsed"`
	SwapPct   float64   `json:"swapPct"`
	Disks     []Disk    `json:"disks"`
	Ifaces    []Iface   `json:"ifaces"`
	Temps     []Temp    `json:"temps"`
	Procs     []Proc    `json:"procs"`
}

// ------------------------------------------------------------------ collector

type cpuTimes struct{ total, idle uint64 }

type collector struct {
	mu      sync.RWMutex
	snap    Snapshot
	history []Point

	lastCPU   map[string]cpuTimes
	lastNet   map[string][2]uint64
	lastProc  map[int]uint64
	lastStamp time.Time

	host, model, kernel string
	diskPaths           []string
}

func newCollector(disks []string) *collector {
	c := &collector{
		lastCPU:   map[string]cpuTimes{},
		lastNet:   map[string][2]uint64{},
		lastProc:  map[int]uint64{},
		diskPaths: disks,
	}
	c.host, _ = os.Hostname()
	c.model = strings.TrimRight(readFile("/proc/device-tree/model"), "\x00\n ")
	if c.model == "" {
		c.model = "unknown board"
	}
	c.kernel = firstField(readFile("/proc/sys/kernel/osrelease"))
	return c
}

func (c *collector) run() {
	c.sample() // prime the deltas
	for range time.Tick(interval) {
		c.sample()
	}
}

func (c *collector) sample() {
	now := time.Now()
	dt := now.Sub(c.lastStamp).Seconds()
	first := c.lastStamp.IsZero()
	c.lastStamp = now

	s := Snapshot{
		Time:   now.UnixMilli(),
		Host:   c.host,
		Model:  c.model,
		Kernel: c.kernel,
		Uptime: parseFloat(firstField(readFile("/proc/uptime"))),
	}

	s.CPU, s.Cores = c.readCPU()
	c.readMem(&s)
	s.Load = readLoad()
	s.Disks = readDisks(c.diskPaths)
	s.Ifaces = c.readNet(dt)
	s.Temps = readTemps()
	s.Procs = c.readProcs(dt)

	c.mu.Lock()
	c.snap = s
	if !first {
		p := Point{T: s.Time, CPU: s.CPU, Mem: s.MemPct}
		for _, f := range s.Ifaces {
			p.Rx += f.Rx
			p.Tx += f.Tx
		}
		for _, t := range s.Temps {
			if t.C > p.Temp {
				p.Temp = t.C
			}
		}
		c.history = append(c.history, p)
		if len(c.history) > historyLen {
			c.history = c.history[len(c.history)-historyLen:]
		}
	}
	c.mu.Unlock()
}

// readCPU returns aggregate busy% and per-core busy%, from /proc/stat deltas.
func (c *collector) readCPU() (float64, []Core) {
	var total float64
	var cores []Core

	for _, line := range strings.Split(readFile("/proc/stat"), "\n") {
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		var sum, idle uint64
		for i, v := range f[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				break
			}
			sum += n
			if i == 3 || i == 4 { // idle + iowait
				idle += n
			}
		}
		name := f[0]
		prev := c.lastCPU[name]
		c.lastCPU[name] = cpuTimes{sum, idle}

		pct := 0.0
		if dTotal := sum - prev.total; prev.total != 0 && dTotal > 0 {
			pct = clamp(100 * float64(dTotal-(idle-prev.idle)) / float64(dTotal))
		}
		if name == "cpu" {
			total = pct
		} else {
			cores = append(cores, Core{Name: name, Pct: pct, MHz: coreMHz(name)})
		}
	}
	return total, cores
}

func coreMHz(cpu string) int {
	p := "/sys/devices/system/cpu/" + cpu + "/cpufreq/scaling_cur_freq"
	if v := firstField(readFile(p)); v != "" {
		return int(parseFloat(v) / 1000) // kHz -> MHz
	}
	return 0
}

func (c *collector) readMem(s *Snapshot) {
	m := map[string]uint64{}
	for _, line := range strings.Split(readFile("/proc/meminfo"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		m[k] = parseUint(firstField(v)) * 1024 // kB -> bytes
	}
	s.MemTotal = m["MemTotal"]
	s.MemAvail = m["MemAvailable"]
	s.MemCache = m["Buffers"] + m["Cached"]
	// "Used" the way free(1) reports it: total - available.
	if s.MemTotal > s.MemAvail {
		s.MemUsed = s.MemTotal - s.MemAvail
	}
	if s.MemTotal > 0 {
		s.MemPct = clamp(100 * float64(s.MemUsed) / float64(s.MemTotal))
	}
	s.SwapTotal = m["SwapTotal"]
	if s.SwapTotal > m["SwapFree"] {
		s.SwapUsed = s.SwapTotal - m["SwapFree"]
	}
	if s.SwapTotal > 0 {
		s.SwapPct = clamp(100 * float64(s.SwapUsed) / float64(s.SwapTotal))
	}
}

func readLoad() []float64 {
	f := strings.Fields(readFile("/proc/loadavg"))
	out := []float64{0, 0, 0}
	for i := 0; i < 3 && i < len(f); i++ {
		out[i] = parseFloat(f[i])
	}
	return out
}

func readDisks(paths []string) []Disk {
	var out []Disk
	for _, p := range paths {
		var st syscall.Statfs_t
		if syscall.Statfs(p, &st) != nil {
			continue
		}
		bs := uint64(st.Bsize)
		total := st.Blocks * bs
		free := st.Bavail * bs
		if total == 0 {
			continue
		}
		d := Disk{Path: p, Total: total, Free: free, Used: total - st.Bfree*bs}
		d.UsedPct = clamp(100 * float64(d.Used) / float64(d.Used+free))
		out = append(out, d)
	}
	return out
}

// readNet reports per-second rates for physical interfaces (lo is skipped).
func (c *collector) readNet(dt float64) []Iface {
	var out []Iface
	for _, line := range strings.Split(readFile("/proc/net/dev"), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || name == "" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, tx := parseUint(f[0]), parseUint(f[8])
		prev, seen := c.lastNet[name]
		c.lastNet[name] = [2]uint64{rx, tx}

		// Skip interfaces that have never carried traffic - the stick has
		// several down-by-default gadget interfaces.
		if rx == 0 && tx == 0 {
			continue
		}
		i := Iface{Name: name, RxTotal: rx, TxTotal: tx}
		if seen && dt > 0 {
			if rx >= prev[0] {
				i.Rx = float64(rx-prev[0]) / dt
			}
			if tx >= prev[1] {
				i.Tx = float64(tx-prev[1]) / dt
			}
		}
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func readTemps() []Temp {
	var out []Temp
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	sort.Strings(zones)
	for _, z := range zones {
		raw := firstField(readFile(z + "/temp"))
		if raw == "" {
			continue
		}
		name := strings.TrimSpace(readFile(z + "/type"))
		out = append(out, Temp{Name: name, C: parseFloat(raw) / 1000})
	}
	return out
}

// readProcs returns the busiest processes by CPU, falling back to RSS order
// when everything is idle.
func (c *collector) readProcs(dt float64) []Proc {
	ents, _ := os.ReadDir("/proc")
	ticks := float64(clockTicks)
	seen := make(map[int]uint64, len(ents))
	var all []Proc

	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat := readFile("/proc/" + e.Name() + "/stat")
		rp := strings.LastIndex(stat, ")")
		lp := strings.Index(stat, "(")
		if rp < 0 || lp < 0 || rp < lp {
			continue
		}
		name := stat[lp+1 : rp]
		f := strings.Fields(stat[rp+1:])
		if len(f) < 22 {
			continue
		}
		// After the comm field, token i is /proc/pid/stat field i+3.
		used := parseUint(f[11]) + parseUint(f[12]) // utime + stime
		rss := parseUint(f[21]) * uint64(pageSize)
		seen[pid] = used

		p := Proc{PID: pid, Name: name, RSS: rss}
		if prev, ok := c.lastProc[pid]; ok && dt > 0 && used >= prev {
			p.CPU = clamp(100 * (float64(used-prev) / ticks) / dt)
		}
		all = append(all, p)
	}
	c.lastProc = seen

	sort.Slice(all, func(a, b int) bool {
		if all[a].CPU != all[b].CPU {
			return all[a].CPU > all[b].CPU
		}
		return all[a].RSS > all[b].RSS
	})
	if len(all) > topProcs {
		all = all[:topProcs]
	}
	return all
}

// -------------------------------------------------------------------- helpers

var (
	clockTicks = 100 // USER_HZ; 100 on every Linux/arm64 build in practice
	pageSize   = os.Getpagesize()
)

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func parseFloat(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
func parseUint(s string) uint64   { v, _ := strconv.ParseUint(s, 10, 64); return v }

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// ----------------------------------------------------------------------- main

func main() {
	addr := flag.String("listen", ":8080", "address to listen on")
	disks := flag.String("disks", "/,/boot", "comma-separated mount points to report")
	flag.Parse()

	c := newCollector(strings.Split(*disks, ","))
	go c.run()

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(v)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	http.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		s := c.snap
		c.mu.RUnlock()
		writeJSON(w, s)
	})
	http.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		h := append([]Point(nil), c.history...)
		c.mu.RUnlock()
		writeJSON(w, h)
	})

	log.Printf("gomon listening on %s", *addr)
	srv := &http.Server{
		Addr:         *addr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
