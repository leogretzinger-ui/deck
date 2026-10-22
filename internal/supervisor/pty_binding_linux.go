package supervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	PTYBindingSchemaV1 = "noisedeck.supervisor-pty-binding.v1"
	// bootIDPath is the fixed supervisor-host kernel source of boot identity.
	bootIDPath = "/proc/sys/kernel/random/boot_id"

	legacyPTYSlaveMajor = 3
	unix98PTYSlaveMajor = 136
	unix98PTYMajorCount = 8
	linuxDeviceMajorMax = 1<<12 - 1
	linuxDeviceMinorMax = 1<<20 - 1
)

var (
	ErrInvalidPTYBinding     = errors.New("invalid supervisor PTY binding")
	ErrPTYBindingDigest      = errors.New("supervisor PTY binding digest mismatch")
	ErrPTYBindingEncoding    = errors.New("supervisor PTY binding is not canonical")
	ErrPTYBindingMeasurement = errors.New("supervisor PTY measurement failed")
	// ErrPTYBindingUnavailable marks a host that cannot yield a measured
	// binding at all: no kernel statx, no effective STATX_MNT_ID, or no
	// boot identity. Admission must fail closed on it.
	ErrPTYBindingUnavailable = errors.New("supervisor PTY measurement unavailable")
)

// PTYBindingV1 is the closed descriptor-derived identity of one PTY slave as
// observed through three open terminal descriptors. It carries kernel facts
// only: no path, numeric descriptor, process id, caller label, terminal
// bytes, or provider content can be represented here.
type PTYBindingV1 struct {
	Schema              string `json:"schema"`
	BootID              string `json:"boot_id"`
	MountID             uint64 `json:"mount_id"`
	CharDeviceMajor     uint32 `json:"char_device_major"`
	CharDeviceMinor     uint32 `json:"char_device_minor"`
	Inode               uint64 `json:"inode"`
	TerminalDeviceMajor uint32 `json:"terminal_device_major"`
	TerminalDeviceMinor uint32 `json:"terminal_device_minor"`
}

// PTYBindingRevision is the exact binding identity retained by the
// supervisor and compared by the admission endpoint.
type PTYBindingRevision struct {
	Digest        string
	CanonicalJSON string
	Binding       PTYBindingV1
}

func CanonicalizePTYBinding(binding PTYBindingV1) (*PTYBindingRevision, error) {
	if err := validatePTYBinding(binding); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return nil, ErrInvalidPTYBinding
	}
	digest := sha256.Sum256(raw)
	return &PTYBindingRevision{
		Digest:        hex.EncodeToString(digest[:]),
		CanonicalJSON: string(raw),
		Binding:       binding,
	}, nil
}

// VerifyPTYBinding is the authority-bearing decode path for a retained or
// received binding. It rejects unknown, duplicate, reordered, trailing, or
// otherwise non-canonical JSON rather than normalizing it into a different
// identity.
func VerifyPTYBinding(digest, canonicalJSON string) (*PTYBindingRevision, error) {
	if !isLowerSHA256(digest) {
		return nil, ErrPTYBindingDigest
	}
	var binding PTYBindingV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return nil, ErrPTYBindingEncoding
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrPTYBindingEncoding
	}
	revision, err := CanonicalizePTYBinding(binding)
	if err != nil {
		return nil, err
	}
	if revision.CanonicalJSON != canonicalJSON {
		return nil, ErrPTYBindingEncoding
	}
	if revision.Digest != digest {
		return nil, ErrPTYBindingDigest
	}
	return revision, nil
}

func validatePTYBinding(binding PTYBindingV1) error {
	if binding.Schema != PTYBindingSchemaV1 {
		return ErrInvalidPTYBinding
	}
	if !isCanonicalBootID(binding.BootID) {
		return ErrInvalidPTYBinding
	}
	// Mount id and inode zero are reserved. Device components must survive the
	// kernel's dev_t encoding without truncation.
	if binding.MountID == 0 {
		return ErrInvalidPTYBinding
	}
	if binding.Inode == 0 {
		return ErrInvalidPTYBinding
	}
	if !isRepresentableDevice(binding.CharDeviceMajor, binding.CharDeviceMinor) {
		return ErrInvalidPTYBinding
	}
	if !isRepresentableDevice(binding.TerminalDeviceMajor, binding.TerminalDeviceMinor) ||
		!isPTYSlaveDevice(binding.TerminalDeviceMajor) {
		return ErrInvalidPTYBinding
	}
	return nil
}

// isLinuxRepresentableDescriptor reports whether value can name a Linux
// descriptor at all. The kernel fd argument is a 32-bit signed int, so a
// wider Go int would alias a real descriptor through syscall truncation;
// distinct Go values that truncate to the same fd must fail closed before
// any syscall.
func isLinuxRepresentableDescriptor(fd int) bool {
	return fd >= 0 && fd <= math.MaxInt32
}

// MeasurePTYBinding derives the binding of the one PTY slave that must stand
// behind the three caller-owned descriptors given in stdin/stdout/stderr
// order. It never closes them. Identity comes only from kernel facts about
// the open descriptors themselves: no /dev/pts index, /proc fd link, ttyname,
// or caller or path claim is consulted.
func MeasurePTYBinding(stdin, stdout, stderr int) (*PTYBindingRevision, error) {
	return measurePTYBinding(stdin, stdout, stderr, nil)
}

func measurePTYBinding(stdin, stdout, stderr int, afterRetain func()) (*PTYBindingRevision, error) {
	for _, fd := range [3]int{stdin, stdout, stderr} {
		if !isLinuxRepresentableDescriptor(fd) {
			return nil, fmt.Errorf("%w: descriptor is not a Linux fd", ErrPTYBindingMeasurement)
		}
	}
	if stdin == stdout || stdin == stderr || stdout == stderr {
		return nil, fmt.Errorf("%w: descriptors are not distinct", ErrPTYBindingMeasurement)
	}
	originals := [3]int{stdin, stdout, stderr}
	// Retained copies pin one open file description per position so a caller
	// that closes or reuses its descriptors mid-measurement cannot splice two
	// objects into one binding. The duplicates are the only descriptors
	// measurement ever closes; the defer covers every partial-failure return.
	stable := [3]int{-1, -1, -1}
	defer func() {
		for _, fd := range stable {
			if fd >= 0 {
				_ = unix.Close(fd)
			}
		}
	}()
	for i, fd := range originals {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			return nil, fmt.Errorf("%w: descriptor is not open", ErrPTYBindingMeasurement)
		}
		if flags&unix.FD_CLOEXEC == 0 {
			return nil, fmt.Errorf("%w: descriptor is not close-on-exec", ErrPTYBindingMeasurement)
		}
		stable[i], err = unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("%w: descriptor cannot be retained", ErrPTYBindingMeasurement)
		}
	}
	if afterRetain != nil {
		afterRetain()
	}

	bootID, err := currentBootID()
	if err != nil {
		return nil, err
	}
	facts, err := measureDescriptorPTYFacts(stable[0])
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}
	for _, descriptor := range []struct {
		name string
		fd   int
	}{{"stdout", stable[1]}, {"stderr", stable[2]}} {
		other, err := measureDescriptorPTYFacts(descriptor.fd)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", descriptor.name, err)
		}
		if other != facts {
			return nil, fmt.Errorf("%w: %s is not the same PTY slave", ErrPTYBindingMeasurement, descriptor.name)
		}
	}
	return CanonicalizePTYBinding(PTYBindingV1{
		Schema:              PTYBindingSchemaV1,
		BootID:              bootID,
		MountID:             facts.mountID,
		CharDeviceMajor:     facts.charDeviceMajor,
		CharDeviceMinor:     facts.charDeviceMinor,
		Inode:               facts.inode,
		TerminalDeviceMajor: facts.terminalDeviceMajor,
		TerminalDeviceMinor: facts.terminalDeviceMinor,
	})
}

// descriptorPTYFacts is the comparable kernel identity of one open
// descriptor; equality across descriptors means the same PTY slave. The char
// device fields are the statx st_dev of the filesystem holding the node; the
// terminal device fields are the statx rdev cross-checked against TIOCGDEV.
type descriptorPTYFacts struct {
	mountID             uint64
	charDeviceMajor     uint32
	charDeviceMinor     uint32
	inode               uint64
	terminalDeviceMajor uint32
	terminalDeviceMinor uint32
}

func measureDescriptorPTYFacts(fd int) (descriptorPTYFacts, error) {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return descriptorPTYFacts{}, fmt.Errorf("%w: descriptor is not open", ErrPTYBindingMeasurement)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		return descriptorPTYFacts{}, fmt.Errorf("%w: descriptor is not close-on-exec", ErrPTYBindingMeasurement)
	}

	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat); err != nil {
		if errors.Is(err, unix.ENOSYS) {
			return descriptorPTYFacts{}, fmt.Errorf("%w: kernel statx", ErrPTYBindingUnavailable)
		}
		return descriptorPTYFacts{}, fmt.Errorf("%w: statx", ErrPTYBindingMeasurement)
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return descriptorPTYFacts{}, fmt.Errorf("%w: kernel mount identity", ErrPTYBindingUnavailable)
	}
	if stat.Mask&(unix.STATX_TYPE|unix.STATX_MODE|unix.STATX_INO) != unix.STATX_TYPE|unix.STATX_MODE|unix.STATX_INO {
		return descriptorPTYFacts{}, fmt.Errorf("%w: statx mask", ErrPTYBindingMeasurement)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
		return descriptorPTYFacts{}, fmt.Errorf("%w: not a character device", ErrPTYBindingMeasurement)
	}

	if _, err := unix.IoctlGetTermios(fd, unix.TCGETS); err != nil {
		return descriptorPTYFacts{}, fmt.Errorf("%w: not a terminal", ErrPTYBindingMeasurement)
	}
	// TIOCGPTN succeeds only on a PTY master; any other failure keeps the
	// descriptor eligible, anything else is ambiguous and fails closed.
	if _, err := unix.IoctlGetInt(fd, unix.TIOCGPTN); err == nil {
		return descriptorPTYFacts{}, fmt.Errorf("%w: PTY master", ErrPTYBindingMeasurement)
	} else if !errors.Is(err, unix.ENOTTY) {
		return descriptorPTYFacts{}, fmt.Errorf("%w: PTY master check", ErrPTYBindingMeasurement)
	}
	// TIOCGDEV reports the terminal's device in the kernel's new_encode_dev
	// form; it must agree with the terminal device number from statx. TCGETS
	// already proved a terminal, so ENOTTY here means the kernel predates
	// the ioctl and cannot measure terminal-device identity at all.
	encoded, err := unix.IoctlGetInt(fd, unix.TIOCGDEV)
	if errors.Is(err, unix.ENOTTY) {
		return descriptorPTYFacts{}, fmt.Errorf("%w: kernel terminal device identity", ErrPTYBindingUnavailable)
	}
	if err != nil {
		return descriptorPTYFacts{}, fmt.Errorf("%w: terminal device", ErrPTYBindingMeasurement)
	}
	// TIOCGDEV writes a 32-bit unsigned device encoding; a negative value or
	// one beyond uint32 cannot be a kernel device number and fails closed.
	if encoded < 0 || int64(encoded) > math.MaxUint32 {
		return descriptorPTYFacts{}, fmt.Errorf("%w: terminal device", ErrPTYBindingMeasurement)
	}
	// #nosec G115 -- the range check above admits only uint32-representable
	// values, so the conversion cannot wrap.
	major, minor := decodeKernelDevice(uint32(encoded))
	if major != stat.Rdev_major || minor != stat.Rdev_minor {
		return descriptorPTYFacts{}, fmt.Errorf("%w: terminal device disagreement", ErrPTYBindingMeasurement)
	}
	if !isPTYSlaveDevice(major) {
		return descriptorPTYFacts{}, fmt.Errorf("%w: not a PTY slave", ErrPTYBindingMeasurement)
	}

	return descriptorPTYFacts{
		mountID:             stat.Mnt_id,
		charDeviceMajor:     stat.Dev_major,
		charDeviceMinor:     stat.Dev_minor,
		inode:               stat.Ino,
		terminalDeviceMajor: stat.Rdev_major,
		terminalDeviceMinor: stat.Rdev_minor,
	}, nil
}

// decodeKernelDevice reverses the kernel's new_encode_dev used by TIOCGDEV.
func decodeKernelDevice(encoded uint32) (major, minor uint32) {
	major = (encoded & 0xfff00) >> 8
	minor = (encoded & 0xff) | ((encoded >> 12) & 0xfff00)
	return major, minor
}

func isRepresentableDevice(major, minor uint32) bool {
	return major <= linuxDeviceMajorMax && minor <= linuxDeviceMinorMax
}

func isPTYSlaveDevice(major uint32) bool {
	return major == legacyPTYSlaveMajor ||
		(major >= unix98PTYSlaveMajor && major < unix98PTYSlaveMajor+unix98PTYMajorCount)
}

func currentBootID() (string, error) {
	raw, err := os.ReadFile(bootIDPath)
	if err != nil {
		return "", fmt.Errorf("%w: boot identity", ErrPTYBindingUnavailable)
	}
	bootID := strings.TrimSuffix(string(raw), "\n")
	if !isCanonicalBootID(bootID) {
		return "", fmt.Errorf("%w: boot identity", ErrPTYBindingUnavailable)
	}
	return bootID, nil
}

// isCanonicalBootID accepts canonical lower-case UUID syntax; the kernel
// does not promise one UUID version, so no version or variant nibble is
// assumed.
func isCanonicalBootID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' ||
		value[18] != '-' || value[23] != '-' {
		return false
	}
	for i := 0; i < len(value); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return false
		}
	}
	return true
}
