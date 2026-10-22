// Package sealedstate decides whether the stores Deck writes to are backed
// by storage the chosen profile accepts, from inside the container.
//
// The container's view is limited. It sees its own mount table, the FUSE
// options on each mount, the kernel's FUSE parameter, its swap table and,
// when the host exposes it, /sys/dev/block; it does not see the host's btrfs
// device list or the host's mount table, so a store on a btrfs subvolume
// looks like an anonymous device it cannot classify. The host preflight
// (deploy/sealed-preflight) has the full view and, after the container is
// created, writes a report from the container's own mount table. When
// encrypted state is required this package needs that report for every
// store: same kernel boot, same container, same profile, and for each store
// the same mount id, device and root the container sees now, with a backing
// the profile accepts. What the container can classify itself must agree
// with the report; what it cannot is settled by the report. The report
// authorizes nothing by itself, and neither does what the container sees
// alone, because a mount that looks sealed from inside can still be one the
// host never qualified.
//
// No environment value and no file other than the report is consulted, so
// a marker file or an exported variable can never turn plaintext into a pass.
package sealedstate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Profile names the storage layout the deployment claims.
type Profile string

const (
	// ProfileShared is the partner's Deck on a host the other party
	// administers: gocryptfs owned by the partner uid and nothing else. A
	// dm-crypt device does not count here, because the other party
	// unlocked it and reads its plaintext; a tmpfs does not count, because
	// root reads a container tmpfs through /proc/<pid>/root.
	ProfileShared Profile = "shared-host"
	// ProfileOwn is whole-disk LUKS on a host the partner administers alone.
	// dm-crypt passes and a tmpfs /tmp passes as temporary memory.
	// Swap policy is selected independently of this layout.
	ProfileOwn Profile = "own-host"
)

// Backing is what a store's mount was resolved to.
type Backing string

const (
	BackingDMCrypt    Backing = "dm-crypt"
	BackingGocryptfs  Backing = "gocryptfs"
	BackingTmpfs      Backing = "tmpfs"
	BackingPlaintext  Backing = "plaintext"
	BackingUnresolved Backing = "unresolved"
)

// accepted says which backings a profile treats as sealed for a store. The
// tmpfs entry is further limited to the tmp store by classify.
var accepted = map[Profile]map[Backing]bool{
	ProfileShared: {BackingGocryptfs: true},
	ProfileOwn:    {BackingDMCrypt: true, BackingTmpfs: true},
}

// Store is one path Deck persists into. Mounts nested below a store are
// discovered from the mount table and checked as stores of their own.
type Store struct {
	Name string
	Path string
}

// DefaultStores are the container paths Deck writes to. Anything bound below
// them, such as the palace or the GitHub config, is found by walking the
// mount table and does not need naming here.
func DefaultStores() []Store {
	return []Store{
		{Name: "data", Path: "/data"},
		{Name: "home", Path: "/home/agent"},
		{Name: "projects", Path: "/workspace"},
		{Name: "tmp", Path: "/tmp"},
	}
}

// Mount is one line of /proc/self/mountinfo, reduced to what the check uses.
type Mount struct {
	ID         int
	Device     string // major:minor
	Root       string
	MountPoint string
	FSType     string
	Source     string
	SuperOpts  string
}

// ParseMountInfo reads mountinfo(5) lines.
func ParseMountInfo(r io.Reader) ([]Mount, error) {
	var mounts []Mount
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || sep+2 >= len(fields) {
			return nil, fmt.Errorf("sealedstate: malformed mountinfo line: %q", line)
		}
		id, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("sealedstate: malformed mount id in %q", line)
		}
		m := Mount{
			ID:         id,
			Device:     fields[2],
			Root:       unescape(fields[3]),
			MountPoint: unescape(fields[4]),
			FSType:     fields[sep+1],
			Source:     unescape(fields[sep+2]),
		}
		if sep+3 < len(fields) {
			m.SuperOpts = fields[sep+3]
		}
		mounts = append(mounts, m)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("sealedstate: read mountinfo: %w", err)
	}
	return mounts, nil
}

// unescape undoes the octal escapes mountinfo uses for space, tab, newline
// and backslash in paths.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if c, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(c))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// MountFor returns the mount that holds path: the mount point that is the
// longest prefix of it. A later line wins over an earlier one at the same
// mount point, matching how the kernel stacks mounts.
func MountFor(mounts []Mount, path string) (Mount, bool) {
	path = filepath.Clean(path)
	var best Mount
	found := false
	bestLen := -1
	for _, m := range mounts {
		mp := filepath.Clean(m.MountPoint)
		if mp != "/" && path != mp && !strings.HasPrefix(path, mp+"/") {
			continue
		}
		if len(mp) >= bestLen {
			best, bestLen, found = m, len(mp), true
		}
	}
	return best, found
}

// superOpts splits a mount's per-superblock options into a map.
func superOpts(m Mount) map[string]string {
	opts := map[string]string{}
	for _, item := range strings.Split(m.SuperOpts, ",") {
		if item == "" {
			continue
		}
		k, v, _ := strings.Cut(item, "=")
		opts[k] = v
	}
	return opts
}

// Report is what the host preflight writes after it has verified the
// container's own mounts against the host sources it qualified.
type Report struct {
	BootID      string                 `json:"boot_id"`
	ContainerID string                 `json:"container_id"`
	Profile     Profile                `json:"profile"`
	SwapPolicy  SwapPolicy             `json:"swap_policy,omitempty"`
	Stores      map[string]ReportStore `json:"stores"`
}

// ReportStore is one store as the host saw it in the container's mount table.
type ReportStore struct {
	Path    string  `json:"path"`
	MountID int     `json:"mount_id"`
	Device  string  `json:"device"`
	Root    string  `json:"root"`
	Backing Backing `json:"backing"`
}

// Facts are the container-side inputs. Tests supply them from fixtures; the
// binary reads them from the kernel.
type Facts struct {
	Mounts      []Mount
	BootID      string
	ContainerID string
	// UID is the uid the container runs as; a gocryptfs mount must be owned
	// by it, because FUSE's owner-only rule is what denies root the view.
	UID int
	// DMUUID returns the device-mapper uuid for a major:minor, or "" when the
	// device is not a dm target or sysfs does not show it.
	DMUUID func(device string) string
	// FuseAllowSysAdminAccess is the kernel parameter as a string; anything
	// but "N" means root reads every FUSE mount regardless of allow_other.
	FuseAllowSysAdminAccess string
	// Swaps lists the active swap devices and files.
	Swaps      []string
	SwapPolicy SwapPolicy
	// SwapDevice maps a swap path to its major:minor, or "".
	SwapDevice func(path string) string
	// ResumeDevice is /sys/power/resume; "0:0" or "" means hibernation is off.
	ResumeDevice string
	Report       *Report
	// ReportErr is why the report could not be read, when it could not.
	ReportErr error
}

// Result is the classification of one store or of a mount nested inside one.
type Result struct {
	Store   Store
	Mount   Mount
	Backing Backing
	// Reason says why the store is not sealed, or how it was settled.
	Reason string
	// Nested is set for a mount discovered below a named store.
	Nested bool
}

// Sealed reports whether the backing is one the profile accepts.
func (r Result) Sealed() bool {
	return r.Backing == BackingDMCrypt || r.Backing == BackingGocryptfs || r.Backing == BackingTmpfs
}

// Check classifies every store and every mount nested below one, applies
// the selected swap policy and hibernation rules, and when required demands a
// matching host report for each store. It returns the results and, when
// required, an error naming every failure. When not required the error is
// nil and the caller logs the results.
func Check(facts Facts, profile Profile, stores []Store, required bool) ([]Result, error) {
	var results []Result
	for _, store := range stores {
		results = append(results, classify(facts, profile, store, false))
		for _, m := range nestedMounts(facts.Mounts, store.Path) {
			nested := Store{Name: store.Name + ":" + m.MountPoint, Path: m.MountPoint}
			results = append(results, classify(facts, profile, nested, true))
		}
	}
	for i := range results {
		settleWithReport(facts, profile, required, &results[i])
	}

	var failures []string
	for _, r := range results {
		if !r.Sealed() {
			failures = append(failures, fmt.Sprintf("%s (%s): %s, %s", r.Store.Name, r.Store.Path, r.Backing, r.Reason))
		}
	}
	failures = append(failures, ProfileFailures(facts, profile)...)
	if !required || len(failures) == 0 {
		return results, nil
	}
	sort.Strings(failures)
	return results, fmt.Errorf("sealedstate: encrypted state is required and these checks failed: %s", strings.Join(failures, "; "))
}

// nestedMounts returns every mount whose mount point lies strictly below
// path, so a plaintext bind inside a sealed store cannot hide behind it.
func nestedMounts(mounts []Mount, path string) []Mount {
	path = filepath.Clean(path)
	var out []Mount
	for _, m := range mounts {
		mp := filepath.Clean(m.MountPoint)
		if mp != path && strings.HasPrefix(mp, path+"/") {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPoint < out[j].MountPoint })
	return out
}

func classify(facts Facts, profile Profile, store Store, nested bool) Result {
	res := Result{Store: store, Nested: nested}
	m, ok := MountFor(facts.Mounts, store.Path)
	if !ok {
		res.Backing = BackingUnresolved
		res.Reason = "no mount holds the path"
		return res
	}
	res.Mount = m
	allowed := accepted[profile]

	switch m.FSType {
	case "fuse.gocryptfs":
		opts := superOpts(m)
		switch {
		case hasOpt(opts, "allow_other"):
			res.Backing, res.Reason = BackingPlaintext, "gocryptfs mounted with allow_other; host root reads it"
		case hasOpt(opts, "allow_root"):
			res.Backing, res.Reason = BackingPlaintext, "gocryptfs mounted with allow_root; host root reads it"
		case opts["user_id"] == "0":
			res.Backing, res.Reason = BackingPlaintext, "gocryptfs mounted by root; host root reads it"
		case opts["user_id"] != strconv.Itoa(facts.UID):
			res.Backing, res.Reason = BackingPlaintext, fmt.Sprintf("gocryptfs mounted by uid %s, not this container's uid %d", opts["user_id"], facts.UID)
		case facts.FuseAllowSysAdminAccess != "N":
			res.Backing, res.Reason = BackingPlaintext, "kernel fuse.allow_sys_admin_access is not N; host root reads every FUSE mount"
		case !allowed[BackingGocryptfs]:
			res.Backing, res.Reason = BackingPlaintext, fmt.Sprintf("gocryptfs is not an accepted backing on the %s profile", profile)
		default:
			res.Backing = BackingGocryptfs
		}
		return res
	case "tmpfs":
		if allowed[BackingTmpfs] && store.Name == "tmp" {
			res.Backing = BackingTmpfs
			return res
		}
		if profile == ProfileShared {
			res.Backing, res.Reason = BackingPlaintext, "tmpfs is readable by host root through /proc/<pid>/root on the shared-host profile"
		} else {
			res.Backing, res.Reason = BackingPlaintext, "tmpfs is accepted only for /tmp"
		}
		return res
	}
	if facts.DMUUID != nil {
		if uuid := facts.DMUUID(m.Device); strings.HasPrefix(uuid, "CRYPT-") {
			if allowed[BackingDMCrypt] {
				res.Backing = BackingDMCrypt
			} else {
				res.Backing, res.Reason = BackingPlaintext, fmt.Sprintf("dm-crypt is not an accepted backing on the %s profile: whoever runs the host unlocked it", profile)
			}
			return res
		} else if uuid != "" {
			res.Backing, res.Reason = BackingPlaintext, "device-mapper target "+m.Device+" is not dm-crypt"
			return res
		}
	}
	res.Backing = BackingUnresolved
	res.Reason = "device " + m.Device + " has no dm-crypt uuid the container can read"
	return res
}

func hasOpt(opts map[string]string, key string) bool {
	_, ok := opts[key]
	return ok
}

// settleWithReport applies the host report to one result. When encrypted
// state is required every store needs a matching entry; an unresolved store
// takes the report's backing, and a store the container classified itself
// must agree with the report. When not required a missing report only
// leaves unresolved stores unresolved.
func settleWithReport(facts Facts, profile Profile, required bool, res *Result) {
	if facts.Report == nil {
		if !required && res.Backing != BackingUnresolved {
			return
		}
		why := "no host report"
		if facts.ReportErr != nil {
			why = "host report unavailable: " + facts.ReportErr.Error()
		}
		res.Backing = BackingUnresolved
		res.Reason = join(res.Reason, why)
		return
	}
	rep := facts.Report
	fail := func(why string) {
		if required || res.Backing == BackingUnresolved {
			res.Backing = BackingUnresolved
		}
		res.Reason = join(res.Reason, why)
	}
	switch {
	case rep.BootID == "" || rep.BootID != facts.BootID:
		fail("host report is from another boot")
		return
	case rep.ContainerID == "" || rep.ContainerID != facts.ContainerID:
		fail("host report is for another container")
		return
	case rep.Profile != profile:
		fail(fmt.Sprintf("host report is for profile %q, not %q", rep.Profile, profile))
		return
	}
	rs, ok := rep.Stores[res.Store.Name]
	if !ok {
		fail("host report does not name this store")
		return
	}
	if filepath.Clean(rs.Path) != filepath.Clean(res.Store.Path) {
		fail("host report names a different path for this store")
		return
	}
	if rs.MountID != res.Mount.ID || rs.Device != res.Mount.Device || filepath.Clean(rs.Root) != filepath.Clean(res.Mount.Root) {
		fail("host report describes a different mount than the container sees now")
		return
	}
	if !accepted[profile][rs.Backing] {
		res.Backing = BackingPlaintext
		res.Reason = join(res.Reason, fmt.Sprintf("host report classifies it as %s, which the %s profile does not accept", rs.Backing, profile))
		return
	}
	switch res.Backing {
	case BackingUnresolved:
		res.Backing = rs.Backing
		res.Reason = "verified by the host report for this boot, container and mount"
	case rs.Backing:
		res.Reason = "verified locally and by the host report"
	default:
		// The container sees one thing and the host verified another; a
		// mount replaced after the report was written looks like this.
		res.Backing = BackingUnresolved
		res.Reason = join(res.Reason, fmt.Sprintf("container sees %s but the host report says %s", res.Backing, rs.Backing))
	}
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// ProfileFailures applies the rules that are not about one store: swap and
// hibernation. The container sees both through /proc and /sys.
func ProfileFailures(facts Facts, profile Profile) []string {
	failures := SwapFailures(facts, facts.SwapPolicy)
	if profile != ProfileShared && profile != ProfileOwn {
		failures = append(failures, fmt.Sprintf("unknown profile %q", profile))
	}
	if facts.Report != nil && facts.Report.SwapPolicy.Effective() != facts.SwapPolicy.Effective() {
		failures = append(failures, "host report swap policy does not match installation")
	}
	if facts.ResumeDevice != "" && facts.ResumeDevice != "0:0" {
		failures = append(failures, "hibernation resume device is set ("+facts.ResumeDevice+")")
	}
	return failures
}

// ReadReport parses a report file.
func ReadReport(path string) (*Report, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the configured report path, bound read-only from the host.
	if err != nil {
		return nil, err
	}
	var rep Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("sealedstate: parse report %s: %w", path, err)
	}
	return &rep, nil
}

// KernelFacts reads the live inputs: the process's own mount table, boot id,
// container id (the hostname Docker assigns), uid, sysfs dm uuids, the FUSE
// parameter, the swap table, the resume device, and the report at
// reportPath if one exists.
func KernelFacts(reportPath string) (Facts, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return Facts{}, fmt.Errorf("sealedstate: %w", err)
	}
	mounts, err := ParseMountInfo(f)
	_ = f.Close()
	if err != nil {
		return Facts{}, err
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Facts{}, fmt.Errorf("sealedstate: read boot id: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		return Facts{}, fmt.Errorf("sealedstate: read container id: %w", err)
	}
	readTrim := func(p string) string {
		raw, err := os.ReadFile(p) // #nosec G304 -- fixed /proc and /sys paths joined with a major:minor or a device name.
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}
	swapFacts, err := ReadSwapFacts()
	if err != nil {
		return Facts{}, err
	}
	facts := Facts{
		Mounts:      mounts,
		BootID:      strings.TrimSpace(string(bootID)),
		ContainerID: strings.TrimSpace(host),
		UID:         os.Getuid(),
		DMUUID: func(device string) string {
			return readTrim(filepath.Join("/sys/dev/block", device, "dm", "uuid"))
		},
		FuseAllowSysAdminAccess: readTrim("/sys/module/fuse/parameters/allow_sys_admin_access"),
		Swaps:                   swapFacts.Swaps,
		SwapDevice:              swapFacts.SwapDevice,
		ResumeDevice:            readTrim("/sys/power/resume"),
	}
	if reportPath != "" {
		rep, err := ReadReport(reportPath)
		switch {
		case err == nil:
			facts.Report = rep
		case errors.Is(err, os.ErrNotExist):
			facts.ReportErr = errors.New("no report at " + reportPath)
		default:
			facts.ReportErr = err
		}
	}
	return facts, nil
}

// DefaultReportPath is where the compose wrapper bind-mounts the host's
// report, read-only, from a host-owned tmpfs.
const DefaultReportPath = "/run/noisedeck/sealed-state/report.json"

// DestinationFailures checks that every configured persistent destination
// lies inside a store the check found sealed. A database path pointed at a
// writable mount outside the four roots would otherwise pass the store
// check and be created in plaintext. Destinations map a name to the path
// as the caller resolved it; the caller resolves symlinks first, because a
// link from inside a store to outside is the same escape.
func DestinationFailures(results []Result, destinations map[string]string) []string {
	var failures []string
	names := make([]string, 0, len(destinations))
	for name := range destinations {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Clean(destinations[name])
		var covering *Result
		bestLen := -1
		for i := range results {
			sp := filepath.Clean(results[i].Store.Path)
			if path != sp && !strings.HasPrefix(path, sp+"/") {
				continue
			}
			if len(sp) > bestLen {
				covering, bestLen = &results[i], len(sp)
			}
		}
		switch {
		case covering == nil:
			failures = append(failures, fmt.Sprintf("configured %s (%s) is outside every sealed store", name, path))
		case !covering.Sealed():
			failures = append(failures, fmt.Sprintf("configured %s (%s) is on %s, which is %s", name, path, covering.Store.Path, covering.Backing))
		}
	}
	return failures
}

// ResolveExisting resolves symlinks in the longest existing prefix of path
// and rejoins the rest, so a destination that does not exist yet is still
// judged by where it would be created.
func ResolveExisting(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for p := path; ; {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}
