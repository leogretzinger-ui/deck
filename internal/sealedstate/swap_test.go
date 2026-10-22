package sealedstate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSwapPoliciesAgainstKernelEvidence(t *testing.T) {
	for _, mode := range []SwapPolicy{"", SwapPersonal, SwapParanoid, "typo"} {
		for _, backing := range []string{"encrypted", "plain", "mixed", "missing-sysfs", "file", "absent"} {
			t.Run(string(mode)+"/"+backing, func(t *testing.T) {
				root := t.TempDir()
				write := func(path, value string) {
					t.Helper()
					path = filepath.Join(root, path)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(value), 0600); err != nil {
						t.Fatal(err)
					}
				}
				table := "Filename\t\t\t\tType\t\tSize\tUsed\tPriority\n"
				if backing != "absent" {
					table += "/dev/mapper/synthetic\tpartition\t1024\t0\t-2\n"
				}
				if backing == "file" {
					table = strings.ReplaceAll(table, "partition", "file")
				}
				if backing == "mixed" {
					table += "/dev/zram0\tpartition\t1024\t0\t-3\n"
				}
				write("swaps", table)
				if backing != "missing-sysfs" {
					write("sys/class/block/dm-0/dm/name", "synthetic")
					write("sys/class/block/dm-0/dev", "253:0")
					uuid := "CRYPT-PLAIN-synthetic"
					if backing == "plain" {
						uuid = "LVM-synthetic"
					}
					write("sys/dev/block/253:0/dm/uuid", uuid)
				}
				facts, err := readSwapFacts(filepath.Join(root, "swaps"), filepath.Join(root, "sys"))
				if err != nil {
					t.Fatal(err)
				}
				failures := SwapFailures(facts, mode)
				wantPass := mode.Effective() == SwapPersonal || (mode == SwapParanoid && backing == "encrypted")
				if (len(failures) == 0) != wantPass {
					t.Fatalf("policy %s backing %s: %v", mode, backing, failures)
				}
			})
		}
	}
}

func TestSwapObservationRefusesMissingOrMalformedTable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "swaps")
	if _, err := readSwapFacts(path, root); err == nil {
		t.Fatal("missing swap observation accepted")
	}
	for _, raw := range []string{"", "not a swap table", "Filename Type Size Used Priority\n/dev/dm-0 partition garbage 0 -2\n", "Filename Type Size Used Priority\n/dev/dm-0 partition 1024 0 -2\n/dev/dm-0 partition 1024 0 -2\n"} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSwapFacts(path, root); err == nil {
			t.Fatal("malformed swap observation accepted")
		}
	}
}

func TestHostSwapTableCanBeObserved(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux kernel observation")
	}
	if _, err := ReadSwapFacts(); err != nil {
		t.Fatal(err)
	}
}

func TestSwapPolicyMismatchRefusesAdmission(t *testing.T) {
	for _, pair := range [][2]SwapPolicy{{SwapPersonal, SwapParanoid}, {SwapParanoid, SwapPersonal}, {SwapParanoid, ""}, {SwapPersonal, "typo"}} {
		t.Run(string(pair[0])+"/"+string(pair[1]), func(t *testing.T) {
			facts := baseFacts(t, gocryptfsMountInfo, dmCryptOn("253:1"))
			facts.SwapPolicy = pair[0]
			facts.Swaps = []string{"/dev/dm-1"}
			facts.SwapDevice = func(string) string { return "253:1" }
			facts.Report = reportFor(t, facts, ProfileShared, BackingGocryptfs)
			facts.Report.SwapPolicy = pair[1]
			if _, err := Check(facts, ProfileShared, DefaultStores(), true); err == nil || !strings.Contains(err.Error(), "swap policy") {
				t.Fatal("mismatched host policy admitted", err)
			}
		})
	}
}
