package supervisor

import (
	"errors"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestPTYBindingGolden(t *testing.T) {
	revision, err := CanonicalizePTYBinding(validPTYBinding())
	if err != nil {
		t.Fatalf("CanonicalizePTYBinding: %v", err)
	}
	const wantJSON = `{"schema":"noisedeck.supervisor-pty-binding.v1","boot_id":"1b4e28ba-2fa1-11d2-883f-0016d3cca427","mount_id":2718,"char_device_major":0,"char_device_minor":643,"inode":9,"terminal_device_major":136,"terminal_device_minor":3}`
	const wantDigest = "cd1bf03a0fd8d46fc9c9282b885b21dfe90b7b6bc84c24d63134f756b2d9e8f9"
	if revision.CanonicalJSON != wantJSON {
		t.Fatalf("canonical JSON = %s, want %s", revision.CanonicalJSON, wantJSON)
	}
	if revision.Digest != wantDigest {
		t.Fatalf("digest = %s, want %s", revision.Digest, wantDigest)
	}
	verified, err := VerifyPTYBinding(revision.Digest, revision.CanonicalJSON)
	if err != nil {
		t.Fatalf("VerifyPTYBinding: %v", err)
	}
	if !reflect.DeepEqual(verified, revision) {
		t.Fatalf("verified = %#v, want %#v", verified, revision)
	}
}

func TestPTYBindingHasOnlyMeasuredFacts(t *testing.T) {
	typeOf := reflect.TypeOf(PTYBindingV1{})
	want := []string{
		"schema",
		"boot_id",
		"mount_id",
		"char_device_major",
		"char_device_minor",
		"inode",
		"terminal_device_major",
		"terminal_device_minor",
	}
	if typeOf.NumField() != len(want) {
		t.Fatalf("field count = %d, want %d", typeOf.NumField(), len(want))
	}
	for i, tag := range want {
		if got := typeOf.Field(i).Tag.Get("json"); got != tag {
			t.Errorf("field %d JSON tag = %q, want %q", i, got, tag)
		}
	}
}

func TestPTYBindingAcceptsAnyKernelUUIDVersion(t *testing.T) {
	for _, bootID := range []string{
		"1b4e28ba-2fa1-11d2-883f-0016d3cca427",
		"123e4567-e89b-42d3-a456-426614174000",
		"019005f7-9e8b-7c2a-b3d4-426614174000",
	} {
		binding := validPTYBinding()
		binding.BootID = bootID
		if _, err := CanonicalizePTYBinding(binding); err != nil {
			t.Errorf("boot id %s: %v", bootID, err)
		}
	}
}

func TestCanonicalizePTYBindingRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PTYBindingV1)
	}{
		{"unsupported schema", func(b *PTYBindingV1) { b.Schema += ".next" }},
		{"uppercase boot id", func(b *PTYBindingV1) { b.BootID = strings.ToUpper(b.BootID) }},
		{"truncated boot id", func(b *PTYBindingV1) { b.BootID = b.BootID[:35] }},
		{"non-hex boot id", func(b *PTYBindingV1) { b.BootID = "1b4e28ba-2fa1-11d2-883f-0016d3cca42z" }},
		{"missing hyphen", func(b *PTYBindingV1) { b.BootID = strings.Replace(b.BootID, "-", "0", 1) }},
		{"zero mount id", func(b *PTYBindingV1) { b.MountID = 0 }},
		{"zero inode", func(b *PTYBindingV1) { b.Inode = 0 }},
		{"zero terminal major", func(b *PTYBindingV1) { b.TerminalDeviceMajor = 0 }},
		{"non-PTY terminal major", func(b *PTYBindingV1) { b.TerminalDeviceMajor = 4 }},
		{"overflowing character major", func(b *PTYBindingV1) { b.CharDeviceMajor = 1 << 12 }},
		{"overflowing character minor", func(b *PTYBindingV1) { b.CharDeviceMinor = 1 << 20 }},
		{"overflowing terminal major", func(b *PTYBindingV1) { b.TerminalDeviceMajor = 1 << 12 }},
		{"overflowing terminal minor", func(b *PTYBindingV1) { b.TerminalDeviceMinor = 1 << 20 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := validPTYBinding()
			test.mutate(&binding)
			if _, err := CanonicalizePTYBinding(binding); !errors.Is(err, ErrInvalidPTYBinding) {
				t.Fatalf("error = %v, want ErrInvalidPTYBinding", err)
			}
		})
	}
}

func TestVerifyPTYBindingRejectsNonCanonicalAuthority(t *testing.T) {
	revision, err := CanonicalizePTYBinding(validPTYBinding())
	if err != nil {
		t.Fatalf("CanonicalizePTYBinding: %v", err)
	}
	tests := []struct {
		name   string
		digest string
		body   string
		want   error
	}{
		{"uppercase digest", strings.ToUpper(revision.Digest), revision.CanonicalJSON, ErrPTYBindingDigest},
		{"short digest", revision.Digest[:63], revision.CanonicalJSON, ErrPTYBindingDigest},
		{"non-hex digest", strings.Repeat("g", 64), revision.CanonicalJSON, ErrPTYBindingDigest},
		{"wrong digest", strings.Repeat("0", 64), revision.CanonicalJSON, ErrPTYBindingDigest},
		{"malformed JSON", revision.Digest, `{`, ErrPTYBindingEncoding},
		{"unknown field", revision.Digest, strings.TrimSuffix(revision.CanonicalJSON, "}") + `,"fd":3}`,
			ErrPTYBindingEncoding},
		{"leading whitespace", revision.Digest, " " + revision.CanonicalJSON, ErrPTYBindingEncoding},
		{"trailing value", revision.Digest, revision.CanonicalJSON + `{}`, ErrPTYBindingEncoding},
		{"duplicate field", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"schema":`, `"schema":"noisedeck.supervisor-pty-binding.v1","schema":`, 1), ErrPTYBindingEncoding},
		{"unsupported schema", revision.Digest, strings.Replace(revision.CanonicalJSON,
			PTYBindingSchemaV1, PTYBindingSchemaV1+".next", 1), ErrInvalidPTYBinding},
		{"reordered fields", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`{"schema":"noisedeck.supervisor-pty-binding.v1","boot_id":"1b4e28ba-2fa1-11d2-883f-0016d3cca427"`,
			`{"boot_id":"1b4e28ba-2fa1-11d2-883f-0016d3cca427","schema":"noisedeck.supervisor-pty-binding.v1"`, 1),
			ErrPTYBindingEncoding},
		{"negative mount id", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"mount_id":2718`, `"mount_id":-1`, 1), ErrPTYBindingEncoding},
		{"overflowing mount id", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"mount_id":2718`, `"mount_id":18446744073709551616`, 1), ErrPTYBindingEncoding},
		{"string mount id", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"mount_id":2718`, `"mount_id":"2718"`, 1), ErrPTYBindingEncoding},
		{"zero mount id", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"mount_id":2718`, `"mount_id":0`, 1), ErrInvalidPTYBinding},
		{"zero inode", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"inode":9`, `"inode":0`, 1), ErrInvalidPTYBinding},
		{"zero terminal major", revision.Digest, strings.Replace(revision.CanonicalJSON,
			`"terminal_device_major":136`, `"terminal_device_major":0`, 1), ErrInvalidPTYBinding},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyPTYBinding(test.digest, test.body)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err.Error() != test.want.Error() {
				t.Fatalf("error text = %q, want stable %q", err, test.want)
			}
		})
	}
}

func TestPTYBindingDigestCoversEveryField(t *testing.T) {
	base := validPTYBinding()
	revision, err := CanonicalizePTYBinding(base)
	if err != nil {
		t.Fatalf("CanonicalizePTYBinding: %v", err)
	}
	mutations := []func(*PTYBindingV1){
		func(b *PTYBindingV1) { b.BootID = "223e4567-e89b-42d3-a456-426614174000" },
		func(b *PTYBindingV1) { b.MountID++ },
		func(b *PTYBindingV1) { b.CharDeviceMajor++ },
		func(b *PTYBindingV1) { b.CharDeviceMinor++ },
		func(b *PTYBindingV1) { b.Inode++ },
		func(b *PTYBindingV1) { b.TerminalDeviceMajor++ },
		func(b *PTYBindingV1) { b.TerminalDeviceMinor++ },
	}
	for i, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		changed, err := CanonicalizePTYBinding(candidate)
		if err != nil {
			t.Fatalf("mutation %d: %v", i, err)
		}
		if changed.Digest == revision.Digest {
			t.Errorf("mutation %d did not change digest", i)
		}
	}
}

// requireMeasurableHost runs one known-good measurement; a host without
// effective kernel support skips every live test on the explicit sentinel.
func requireMeasurableHost(t *testing.T) (master *os.File, slave *os.File) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })
	fds := [3]int{dupDescriptor(t, int(slave.Fd()), true), dupDescriptor(t, int(slave.Fd()), true), dupDescriptor(t, int(slave.Fd()), true)}
	_, err = MeasurePTYBinding(fds[0], fds[1], fds[2])
	switch {
	case errors.Is(err, ErrPTYBindingUnavailable):
		t.Skipf("host cannot measure a PTY binding: %v", err)
	case err != nil:
		t.Fatalf("MeasurePTYBinding on a known-good triple: %v", err)
	}
	for _, fd := range fds {
		requireDescriptorOpen(t, fd)
	}
	return master, slave
}

func TestMeasurePTYBindingSameSlaveAcrossDupedDescriptors(t *testing.T) {
	_, slave := requireMeasurableHost(t)

	first := [3]int{dupDescriptor(t, int(slave.Fd()), true), dupDescriptor(t, int(slave.Fd()), true),
		dupDescriptor(t, int(slave.Fd()), true)}
	revision, err := MeasurePTYBinding(first[0], first[1], first[2])
	if err != nil {
		t.Fatalf("MeasurePTYBinding: %v", err)
	}
	// Fresh duplicates occupy different descriptor numbers yet must bind
	// identically, in any stdin/stdout/stderr order.
	second := [3]int{dupDescriptor(t, int(slave.Fd()), true), dupDescriptor(t, int(slave.Fd()), true),
		dupDescriptor(t, int(slave.Fd()), true)}
	again, err := MeasurePTYBinding(second[2], second[0], second[1])
	if err != nil {
		t.Fatalf("MeasurePTYBinding again: %v", err)
	}
	if again.Digest != revision.Digest {
		t.Fatalf("duped descriptors changed binding: %s then %s", revision.CanonicalJSON, again.CanonicalJSON)
	}

	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatalf("read boot_id: %v", err)
	}
	if revision.Binding.BootID != strings.TrimSuffix(string(raw), "\n") {
		t.Fatalf("boot id = %q, want kernel boot_id %q", revision.Binding.BootID, raw)
	}

	verified, err := VerifyPTYBinding(revision.Digest, revision.CanonicalJSON)
	if err != nil {
		t.Fatalf("VerifyPTYBinding: %v", err)
	}
	if !reflect.DeepEqual(verified, revision) {
		t.Fatalf("verified = %#v, want %#v", verified, revision)
	}
	for _, fd := range first {
		requireDescriptorOpen(t, fd)
	}
	for _, fd := range second {
		requireDescriptorOpen(t, fd)
	}
}

func TestMeasurePTYBindingPinsDescriptorsDuringMeasurement(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	hostileMaster, hostileSlave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { hostileMaster.Close(); hostileSlave.Close() })

	fds := [3]int{
		dupDescriptor(t, int(slave.Fd()), true),
		dupDescriptor(t, int(slave.Fd()), true),
		dupDescriptor(t, int(slave.Fd()), true),
	}
	want, err := MeasurePTYBinding(fds[0], fds[1], fds[2])
	if err != nil {
		t.Fatalf("clean measurement: %v", err)
	}
	revision, err := measurePTYBinding(fds[0], fds[1], fds[2], func() {
		for _, fd := range fds {
			if closeErr := unix.Close(fd); closeErr != nil {
				t.Fatalf("close retained original: %v", closeErr)
			}
		}
		// Reusing the freed descriptor numbers for a different PTY slave must
		// not leak that object into the binding: measurement reads only the
		// duplicates it retained before the caller acted.
		for i := 0; i < 3; i++ {
			dupDescriptor(t, int(hostileSlave.Fd()), true)
		}
	})
	if err != nil {
		t.Fatalf("measurement after caller closes originals: %v", err)
	}
	if revision.Digest != want.Digest {
		t.Fatalf("pinned measurement changed binding: %s then %s", want.CanonicalJSON, revision.CanonicalJSON)
	}
}

func TestMeasurePTYBindingDistinctSlavesBindDifferently(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	_, otherSlave := requireMeasurableHost(t)

	triple := [3]int{dupDescriptor(t, int(slave.Fd()), true), dupDescriptor(t, int(slave.Fd()), true),
		dupDescriptor(t, int(slave.Fd()), true)}
	revision, err := MeasurePTYBinding(triple[0], triple[1], triple[2])
	if err != nil {
		t.Fatalf("MeasurePTYBinding: %v", err)
	}
	otherTriple := [3]int{dupDescriptor(t, int(otherSlave.Fd()), true), dupDescriptor(t, int(otherSlave.Fd()), true),
		dupDescriptor(t, int(otherSlave.Fd()), true)}
	other, err := MeasurePTYBinding(otherTriple[0], otherTriple[1], otherTriple[2])
	if err != nil {
		t.Fatalf("MeasurePTYBinding other: %v", err)
	}
	if other.Digest == revision.Digest {
		t.Fatalf("distinct PTY slaves share binding %s", revision.CanonicalJSON)
	}
}

func TestMeasurePTYBindingRejects(t *testing.T) {
	_, slave := requireMeasurableHost(t)
	otherMaster, otherSlave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { otherMaster.Close(); otherSlave.Close() })

	regular, err := os.CreateTemp(t.TempDir(), "not-a-terminal")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { regular.Close() })

	pipeFds := make([]int, 2)
	if err := unix.Pipe2(pipeFds, unix.O_CLOEXEC); err != nil {
		t.Fatalf("Pipe2: %v", err)
	}
	t.Cleanup(func() { unix.Close(pipeFds[0]); unix.Close(pipeFds[1]) })

	good := func() int { return dupDescriptor(t, int(slave.Fd()), true) }
	closedCase := [3]int{0, good(), good()}
	closed := dupDescriptor(t, int(slave.Fd()), true)
	if err := unix.Close(closed); err != nil {
		t.Fatalf("close: %v", err)
	}
	closedCase[0] = closed
	tests := []struct {
		name string
		fds  func() [3]int
	}{
		{"closed descriptor", func() [3]int { return closedCase }},
		{"mixed slaves", func() [3]int {
			return [3]int{good(), good(), dupDescriptor(t, int(otherSlave.Fd()), true)}
		}},
		{"PTY master", func() [3]int {
			return [3]int{good(), dupDescriptor(t, int(otherMaster.Fd()), true), good()}
		}},
		{"pipe", func() [3]int { return [3]int{good(), pipeFds[1], good()} }},
		{"regular file", func() [3]int { return [3]int{good(), good(), int(regular.Fd())} }},
		{"missing close-on-exec", func() [3]int {
			return [3]int{good(), good(), dupDescriptor(t, int(slave.Fd()), false)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fds := test.fds()
			_, err := MeasurePTYBinding(fds[0], fds[1], fds[2])
			if !errors.Is(err, ErrPTYBindingMeasurement) {
				t.Fatalf("error = %v, want ErrPTYBindingMeasurement", err)
			}
			if errors.Is(err, ErrPTYBindingUnavailable) {
				t.Fatalf("error = %v, must not be the unavailable sentinel", err)
			}
			for _, fd := range fds {
				if fd == closed {
					continue
				}
				requireDescriptorOpen(t, fd)
			}
		})
	}
}

// TestMeasurePTYBindingRejectsBeforeKernelFacts covers rejections decided
// before any statx or ioctl, so they must hold on every kernel without the
// unavailable sentinel: a closed descriptor and a missing FD_CLOEXEC flag.
func TestMeasurePTYBindingRejectsBeforeKernelFacts(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "descriptor")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { regular.Close() })

	open := [3]int{
		dupDescriptor(t, int(regular.Fd()), true),
		dupDescriptor(t, int(regular.Fd()), true),
		dupDescriptor(t, int(regular.Fd()), true),
	}
	closed := dupDescriptor(t, int(regular.Fd()), true)
	if err := unix.Close(closed); err != nil {
		t.Fatalf("close: %v", err)
	}
	tests := []struct {
		name string
		fds  [3]int
	}{
		{"closed descriptor", [3]int{closed, open[1], open[2]}},
		{"missing close-on-exec", [3]int{dupDescriptor(t, int(regular.Fd()), false), open[1], open[2]}},
		{"duplicate descriptors", [3]int{open[0], open[0], open[2]}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := MeasurePTYBinding(test.fds[0], test.fds[1], test.fds[2])
			if !errors.Is(err, ErrPTYBindingMeasurement) {
				t.Fatalf("error = %v, want ErrPTYBindingMeasurement", err)
			}
			if errors.Is(err, ErrPTYBindingUnavailable) {
				t.Fatalf("error = %v, must not be the unavailable sentinel", err)
			}
			for _, fd := range test.fds {
				if fd == closed {
					continue
				}
				requireDescriptorOpen(t, fd)
			}
		})
	}
}

// TestMeasurePTYBindingRejectsUnrepresentableDescriptors covers values that
// cannot name a Linux descriptor: negative ints, and on 64-bit platforms Go
// ints beyond the kernel's 32-bit signed fd argument that would alias a real
// descriptor through syscall truncation. Rejection is decided before any
// syscall, so the caller's real descriptors must survive untouched.
func TestMeasurePTYBindingRejectsUnrepresentableDescriptors(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "descriptor")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { regular.Close() })

	open := [3]int{
		dupDescriptor(t, int(regular.Fd()), true),
		dupDescriptor(t, int(regular.Fd()), true),
		dupDescriptor(t, int(regular.Fd()), true),
	}
	type fdCase struct {
		name string
		fds  [3]int
	}
	tests := []fdCase{
		{"negative stdin", [3]int{-1, open[1], open[2]}},
		{"negative stdout", [3]int{open[0], -1, open[2]}},
		{"negative stderr", [3]int{open[0], open[1], -1}},
		{"most negative stdin", [3]int{-math.MaxInt32 - 1, open[1], open[2]}},
	}
	if strconv.IntSize == 64 {
		// Wraparound values are distinct Go ints that truncate to the same
		// kernel fd; without the representability check they would alias the
		// open descriptors and survive retention.
		var maxFd = int64(math.MaxInt32)
		wrap := int(wideUnit64)
		tests = append(tests,
			fdCase{"wrapped stdin", [3]int{open[0] + wrap, open[1], open[2]}},
			fdCase{"wrapped stdout", [3]int{open[0], open[1] + wrap, open[2]}},
			fdCase{"wrapped stderr", [3]int{open[0], open[1], open[2] + wrap}},
			fdCase{"doubly wrapped stdin", [3]int{open[0] + 2*wrap, open[1], open[2]}},
			fdCase{"all wrapped", [3]int{open[0] + wrap, open[1] + wrap, open[2] + wrap}},
			fdCase{"beyond 32-bit fd range", [3]int{int(maxFd + 1), open[1], open[2]}},
		)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := MeasurePTYBinding(test.fds[0], test.fds[1], test.fds[2])
			if !errors.Is(err, ErrPTYBindingMeasurement) {
				t.Fatalf("error = %v, want ErrPTYBindingMeasurement", err)
			}
			if errors.Is(err, ErrPTYBindingUnavailable) {
				t.Fatalf("error = %v, must not be the unavailable sentinel", err)
			}
			for _, fd := range open {
				requireDescriptorOpen(t, fd)
			}
		})
	}
}

// TestMeasurePTYBindingRejectsWrappedPTYDescriptors is the discriminating
// wraparound case: 64-bit Go ints that truncate to a real close-on-exec PTY
// slave descriptor would, without the representability check, alias that
// slave, survive retention, and produce a binding. They must instead fail
// closed and leave the real caller descriptors untouched.
func TestMeasurePTYBindingRejectsWrappedPTYDescriptors(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("fd wraparound requires a 64-bit int")
	}
	_, slave := requireMeasurableHost(t)

	fds := [3]int{
		dupDescriptor(t, int(slave.Fd()), true),
		dupDescriptor(t, int(slave.Fd()), true),
		dupDescriptor(t, int(slave.Fd()), true),
	}
	wrap := int(wideUnit64)
	tests := []struct {
		name string
		fds  [3]int
	}{
		{"wrapped stdin", [3]int{fds[0] + wrap, fds[1], fds[2]}},
		{"wrapped stdout", [3]int{fds[0], fds[1] + wrap, fds[2]}},
		{"wrapped stderr", [3]int{fds[0], fds[1], fds[2] + wrap}},
		{"doubly wrapped stdin", [3]int{fds[0] + 2*wrap, fds[1], fds[2]}},
		{"all wrapped", [3]int{fds[0] + wrap, fds[1] + wrap, fds[2] + wrap}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			revision, err := MeasurePTYBinding(test.fds[0], test.fds[1], test.fds[2])
			if !errors.Is(err, ErrPTYBindingMeasurement) {
				t.Fatalf("error = %v (revision %#v), want ErrPTYBindingMeasurement", err, revision)
			}
			if errors.Is(err, ErrPTYBindingUnavailable) {
				t.Fatalf("error = %v, must not be the unavailable sentinel", err)
			}
			for _, fd := range fds {
				requireDescriptorOpen(t, fd)
			}
		})
	}
	if _, err := MeasurePTYBinding(fds[0], fds[1], fds[2]); err != nil {
		t.Fatalf("clean measurement after hostile calls: %v", err)
	}
}

// wideUnit64 is a variable rather than a constant so 64-bit wraparound test
// values still compile where int is 32 bits; the tests using it skip there.
var wideUnit64 = int64(1) << 32

// dupDescriptor duplicates fd; the duplicate is caller-owned, never closed by
// measurement, and closed by test cleanup.
func dupDescriptor(t *testing.T, fd int, closeOnExec bool) int {
	t.Helper()
	cmd := unix.F_DUPFD
	if closeOnExec {
		cmd = unix.F_DUPFD_CLOEXEC
	}
	dup, err := unix.FcntlInt(uintptr(fd), cmd, 0)
	if err != nil {
		t.Fatalf("dup descriptor: %v", err)
	}
	t.Cleanup(func() { unix.Close(dup) })
	return dup
}

func requireDescriptorOpen(t *testing.T, fd int) {
	t.Helper()
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
		t.Fatalf("descriptor %d was closed by measurement: %v", fd, err)
	}
}

func validPTYBinding() PTYBindingV1 {
	return PTYBindingV1{
		Schema:              PTYBindingSchemaV1,
		BootID:              "1b4e28ba-2fa1-11d2-883f-0016d3cca427",
		MountID:             2718,
		CharDeviceMajor:     0,
		CharDeviceMinor:     643,
		Inode:               9,
		TerminalDeviceMajor: 136,
		TerminalDeviceMinor: 3,
	}
}
