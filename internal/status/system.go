package status

import (
	"context"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
)

var (
	sysMu    sync.Mutex
	sysCache SystemStats
	sysAt    time.Time
)

// ReadSystemStats returns a cached (≤2s old) host resource snapshot.
func ReadSystemStats() SystemStats {
	sysMu.Lock()
	defer sysMu.Unlock()
	if time.Since(sysAt) < 2*time.Second {
		return sysCache
	}
	var s SystemStats
	if p, err := cpu.PercentWithContext(context.Background(), 0, false); err == nil && len(p) > 0 {
		s.CPU = p[0]
	}
	if v, err := mem.VirtualMemory(); err == nil {
		s.RAM = v.UsedPercent
	}
	if d, err := disk.Usage("/"); err == nil {
		s.DiskFree = d.Free
	}
	if bt, err := host.BootTime(); err == nil {
		s.Uptime = float64(time.Now().Unix() - int64(bt))
	}
	if n, err := gnet.IOCounters(false); err == nil && len(n) > 0 {
		s.NetSent, s.NetRecv = n[0].BytesSent, n[0].BytesRecv
	}
	sysCache, sysAt = s, time.Now()
	return s
}
