package taskmanager

import "math"

func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func rateValid(v *float64) bool        { return v == nil || finiteNonnegative(*v) }
func percentValid(v *float64) bool     { return v == nil || (finiteNonnegative(*v) && *v <= 100) }
func (s Status) valid() bool {
	switch s {
	case Available, Partial, Unavailable, Unsupported, Absent:
		return true
	}
	return false
}
func identityValid(id string, seen map[string]bool) bool {
	if id == "" || len(id) > 512 || seen[id] {
		return false
	}
	seen[id] = true
	return true
}

// Valid checks observations at the IPC boundary before serving them to clients.
func (s DeviceSnapshot) Valid() bool {
	if s.ObservedAt.IsZero() || !finiteNonnegative(s.SampleSeconds) || s.SampleSeconds == 0 ||
		s.CPU.LogicalCores < 1 || !percentValid(s.CPU.UsagePercent) || s.Memory.TotalBytes == 0 || s.Memory.UsedBytes > s.Memory.TotalBytes ||
		!s.Network.Status.valid() || s.Network.Items == nil || len(s.Network.Items) > 4096 ||
		!s.Disks.Status.valid() || s.Disks.Items == nil || len(s.Disks.Items) > 4096 {
		return false
	}
	seen := make(map[string]bool)
	for _, n := range s.Network.Items {
		if !identityValid(n.ID, seen) || n.Name == "" || !rateValid(n.ReceiveBytesPerSecond) || !rateValid(n.TransmitBytesPerSecond) {
			return false
		}
	}
	seen = make(map[string]bool)
	for _, d := range s.Disks.Items {
		if !identityValid(d.ID, seen) || !rateValid(d.ReadBytesPerSecond) || !rateValid(d.WriteBytesPerSecond) {
			return false
		}
	}
	return s.GPU.valid() && s.NPU.valid()
}

func (g AcceleratorGroup) valid() bool {
	if !g.Status.valid() || g.Items == nil || len(g.Items) > 4096 {
		return false
	}
	seen := make(map[string]bool)
	for _, a := range g.Items {
		if !identityValid(a.ID, seen) || a.Name == "" || !a.Status.valid() || !percentValid(a.UsagePercent) || !metricStatusValid(a.UsageStatus, a.UsagePercent != nil) || !metricStatusValid(a.MemoryUsedStatus, a.MemoryUsedBytes != nil) || !metricStatusValid(a.MemoryTotalStatus, a.MemoryTotalBytes != nil) {
			return false
		}
		if a.MemoryUsedBytes != nil && a.MemoryTotalBytes != nil && *a.MemoryUsedBytes > *a.MemoryTotalBytes {
			return false
		}
	}
	return true
}

func (s TaskSnapshot) Valid() bool {
	return !s.ObservedAt.IsZero() && finiteNonnegative(s.SampleSeconds) && s.SampleSeconds > 0 && s.Processes.valid(4096) && s.Services.valid(1024) && s.iconsValid()
}
func (g TaskGroup) valid(limit int) bool {
	if !g.Status.valid() || g.Items == nil || len(g.Items) > limit {
		return false
	}
	seen := make(map[string]bool)
	for _, t := range g.Items {
		if !identityValid(t.ID, seen) || t.Name == "" || t.PID < 0 || !percentValid(t.CPUPercent) || !rateValid(t.ReadBytesPerSecond) || !rateValid(t.WriteBytesPerSecond) {
			return false
		}
	}
	return true
}

func metricStatusValid(status Status, known bool) bool {
	if known {
		return status == Available
	}
	return status == Unsupported || status == Unavailable
}
