package sealedstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SwapPolicy governs memory persisted outside the encrypted workspace.
// Neither policy protects memory from the administrator of a running host.
type SwapPolicy string

const (
	SwapPersonal SwapPolicy = "personal"
	SwapParanoid SwapPolicy = "paranoid"
)

func (p SwapPolicy) Effective() SwapPolicy {
	if p == "" {
		return SwapPersonal
	}
	return p
}

func (p SwapPolicy) Valid() bool { return p.Effective() == SwapPersonal || p == SwapParanoid }

// SwapFailures uses current kernel observations, never a user assertion
// that a device is encrypted. Paranoid mode requires an active encrypted swap.
func SwapFailures(f Facts, p SwapPolicy) []string {
	if !p.Valid() {
		return []string{fmt.Sprintf("unknown swap policy %q", p)}
	}
	if p.Effective() == SwapPersonal {
		return nil
	}
	if len(f.Swaps) == 0 {
		return []string{"paranoid swap policy requires active encrypted swap"}
	}
	var failures []string
	for _, path := range f.Swaps {
		device := ""
		if f.SwapDevice != nil {
			device = f.SwapDevice(path)
		}
		if device == "" || f.DMUUID == nil || !strings.HasPrefix(f.DMUUID(device), "CRYPT-") {
			failures = append(failures, "swap "+path+" is not verified dm-crypt")
		}
	}
	return failures
}

// ReadSwapFacts supports direct dm-crypt swap devices. Swap files and stacked
// mappings fail paranoid admission because the container cannot prove their
// host backing from its own filesystem namespace. No host device is mounted.
func ReadSwapFacts() (Facts, error) { return readSwapFacts("/proc/swaps", "/sys") }

func readSwapFacts(table, sys string) (Facts, error) {
	raw, err := os.ReadFile(table) // #nosec G304 -- fixed kernel path or synthetic test fixture.
	if err != nil {
		return Facts{}, fmt.Errorf("sealedstate: read swap table: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	header := strings.Fields(lines[0])
	if len(header) != 5 || header[0] != "Filename" || header[1] != "Type" {
		return Facts{}, fmt.Errorf("sealedstate: invalid swap table")
	}
	read := func(path string) string {
		b, err := os.ReadFile(path) // #nosec G304 -- kernel sysfs attributes or synthetic test fixture.
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	devices := map[string]string{}
	facts := Facts{SwapDevice: func(path string) string { return devices[path] }, DMUUID: func(device string) string {
		return read(filepath.Join(sys, "dev/block", device, "dm/uuid"))
	}}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) != 5 {
			return Facts{}, fmt.Errorf("sealedstate: invalid swap entry")
		}
		for _, field := range fields[2:4] {
			if _, err := strconv.ParseUint(field, 10, 64); err != nil {
				return Facts{}, fmt.Errorf("sealedstate: invalid swap size")
			}
		}
		path := unescape(fields[0])
		if _, exists := devices[path]; exists {
			return Facts{}, fmt.Errorf("sealedstate: duplicate swap entry")
		}
		facts.Swaps = append(facts.Swaps, path)
		devices[path] = ""
		if fields[1] != "partition" || filepath.Clean(path) != path {
			continue
		}
		if filepath.Dir(path) == "/dev" {
			devices[path] = read(filepath.Join(sys, "class/block", filepath.Base(path), "dev"))
		} else if filepath.Dir(path) == "/dev/mapper" {
			entries, _ := filepath.Glob(filepath.Join(sys, "class/block/dm-*"))
			for _, entry := range entries {
				if read(filepath.Join(entry, "dm/name")) == filepath.Base(path) {
					devices[path] = read(filepath.Join(entry, "dev"))
					break
				}
			}
		}
	}
	return facts, nil
}
