package harnesslogin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLaunchRequiresShippedVersionAndPreservesCustomBrowser(t *testing.T) {
	dir := t.TempDir()
	opener := filepath.Join(dir, "opener")
	native := filepath.Join(dir, "dist", "bundle", "cli.js")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	writePiModuleFixtures(t, dir, piVersion)
	for _, p := range []string{opener, native} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+piVersion+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BROWSER", "")
	call := func(provider string, env []string) []string {
		return launchEnvironment(context.Background(), provider, native, "/fixture/register.sock", env, opener)
	}
	env := call("pi", nil)
	if !slices.Contains(env, "BROWSER="+opener) || !slices.Contains(env, "NOISEDECK_LOGIN_SOCKET=/fixture/register.sock") {
		t.Fatal("reviewed launch missing opener")
	}
	for _, name := range []string{"codex", "cline", "shell", "unknown"} {
		if len(call(name, nil)) != 0 {
			t.Fatal("unreviewed version or harness received opener")
		}
	}
	for _, extra := range [][]string{{"PATH=/custom"}, {"BROWSER=/custom"}} {
		if len(call("pi", extra)) != 0 {
			t.Fatal("custom descriptor environment replaced")
		}
	}
	t.Setenv("BROWSER", "/custom/browser")
	if len(call("pi", nil)) != 0 {
		t.Fatal("the user's own browser was replaced")
	}
}

func TestMissingOpenerAndUnboundedVersionOutputStayNative(t *testing.T) {
	dir := t.TempDir()
	native := filepath.Join(dir, "native")
	if err := os.WriteFile(native, []byte("#!/bin/sh\nwhile true; do echo unexpected; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", "")
	if env := launchEnvironment(context.Background(), "pi", native, "/fixture.sock", nil, filepath.Join(dir, "missing")); len(env) != 0 {
		t.Fatal("missing opener enabled handoff")
	}
	if env := launchEnvironment(context.Background(), "codex", native, "/fixture.sock", nil, native); len(env) != 0 {
		t.Fatal("oversized version output enabled handoff")
	}
}

func writePiModuleFixtures(t *testing.T, root, oauthVersion string) {
	t.Helper()
	for path, text := range map[string]string{
		filepath.Join(root, "package.json"):                                             `{"name":"@earendil-works/pi-coding-agent","version":"` + piVersion + `","dependencies":{"@earendil-works/pi-ai":"^` + piVersion + `"}}`,
		filepath.Join(root, "node_modules", "@earendil-works", "pi-ai", "package.json"): `{"name":"@earendil-works/pi-ai","version":"` + oauthVersion + `"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPiAuthModuleVersionMustSatisfyShippedContract(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "dist", "bundle", "cli.js")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\necho "+piVersion+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	writePiModuleFixtures(t, root, "0.85.2")
	if shippedPiModules(executable) {
		t.Fatal("unreviewed OAuth dependency admitted under a reviewed CLI version")
	}
	writePiModuleFixtures(t, root, piVersion)
	if !shippedPiModules(executable) {
		t.Fatal("reviewed native module installation rejected")
	}
}

func TestStableCaretVersion(t *testing.T) {
	for _, test := range []struct {
		minimum string
		version string
		want    bool
	}{
		{"1.1.0", "1.1.0", true},
		{"1.1.0", "1.1.1", true},
		{"1.1.0", "1.2.0", true},
		{"1.1.0", "1.99.0", true},
		{"1.1.0", "1.0.99", false},
		{"1.1.0", "0.85.2", false},
		{"1.1.0", "2.0.0", false},
		{"1.1.1", "1.1.0", false},
		{"0.2.5", "0.2.5", true},
		{"0.2.5", "0.2.6", true},
		{"0.2.5", "0.2.4", false},
		{"0.2.5", "0.3.0", false},
		{"0.2.5", "1.2.5", false},
		{"0.0.4", "0.0.4", true},
		{"0.0.4", "0.0.5", false},
		{"0.0.4", "0.1.0", false},
		{"0.0.0", "0.0.0", true},
		{"0.0.0", "0.0.1", false},
		{"1.1.0", "1.2.0-beta.1", false},
		{"1.1.0", "1.2.0+build", false},
		{"1.1.0", "01.2.0", false},
		{"1.1.0", "1.02.0", false},
		{"1.1.0", "1.2.00", false},
		{"1.1.0", "v1.2.0", false},
		{"1.1.0", "1.2", false},
		{"1.1.0", "1.2.0.0", false},
		{"1.1.0", "1..0", false},
		{"1.1.0", "1.2.-1", false},
		{"1.1.0", "1.2.+1", false},
		{"1.1.0", " 1.2.0", false},
		{"1.1.0", "1.2.0\n", false},
		{"1.1.0", "１.2.0", false},
		{"1.1.0", "", false},
		{"1.1.0", "1.2.18446744073709551615", true},
		{"1.1.0", "1.2.18446744073709551616", false},
		{"1.1.0", "1.18446744073709551616.0", false},
		{"1.1.0", "18446744073709551616.1.0", false},
		{"18446744073709551615.1.0", "18446744073709551615.2.0", true},
		{"0.18446744073709551615.0", "0.18446744073709551615.1", true},
		{"0.0.18446744073709551615", "0.0.18446744073709551615", true},
		{"01.1.0", "1.2.0", false},
		{"1.1.0-beta", "1.2.0", false},
		{"1.1.18446744073709551616", "1.2.0", false},
	} {
		t.Run(test.minimum+"/"+test.version, func(t *testing.T) {
			if got := stableCaretVersion(test.version, test.minimum); got != test.want {
				t.Fatalf("stableCaretVersion(%q, %q) = %v, want %v", test.version, test.minimum, got, test.want)
			}
		})
	}
}

func TestPiExactDependencyRequiresExactInstalledVersion(t *testing.T) {
	pin, ok := stableVersion(piVersion)
	if !ok {
		t.Fatal("shipped Pi pin is not stable")
	}
	newer := fmt.Sprintf("%d.%d.%d", pin[0], pin[1], pin[2]+1)
	if !stableCaretVersion(newer, piVersion) {
		t.Fatal("fixture must otherwise satisfy the caret range")
	}
	for _, version := range []string{piVersion, newer, piVersion + "-beta.1", piVersion + "+build", " " + piVersion} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			writePiModuleFixtures(t, root, version)
			manifest := filepath.Join(root, "package.json")
			raw, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifest, []byte(strings.Replace(string(raw), "^"+piVersion, piVersion, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(root, "dist", "bundle", "cli.js")
			if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if got := shippedPiModules(executable); got != (version == piVersion) {
				t.Fatalf("exact declaration admitted installed %q: %v", version, got)
			}
		})
	}
}

func TestPiPackageAdmissionAndLaunch(t *testing.T) {
	t.Setenv("BROWSER", "")
	pin, ok := stableVersion(piVersion)
	if !ok {
		t.Fatal("shipped Pi pin must be a canonical stable version")
	}
	format := func(version [3]uint64) string {
		return fmt.Sprintf("%d.%d.%d", version[0], version[1], version[2])
	}
	compatible := pin
	if pin[0] != 0 {
		compatible = [3]uint64{pin[0], pin[1] + 1, 0}
	} else {
		compatible[2]++
	}
	compatibleOK := pin[0] != 0 || pin[1] != 0
	incompatible := pin
	if pin[0] != 0 {
		incompatible = [3]uint64{pin[0] + 1, 0, 0}
	} else if pin[1] != 0 {
		incompatible = [3]uint64{0, pin[1] + 1, 0}
	} else {
		incompatible[2]++
	}
	below := pin
	belowOK := false
	for i := len(below) - 1; i >= 0; i-- {
		if below[i] != 0 {
			below[i]--
			belowOK = true
			break
		}
	}
	cli := `{"name":"@earendil-works/pi-coding-agent","version":"` + piVersion + `","dependencies":{"@earendil-works/pi-ai":"^` + piVersion + `"}}`
	ai := func(version string) string {
		data, err := json.Marshal(map[string]string{"name": "@earendil-works/pi-ai", "version": version})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, test := range []struct {
		name    string
		target  string
		content string
		want    bool
	}{
		{"equal", "ai", ai(piVersion), true},
		{"compatible different", "ai", ai(format(compatible)), compatibleOK},
		{"old dependency", "ai", ai("0.85.2"), false},
		{"below minimum", "ai", ai(format(below)), !belowOK},
		{"incompatible caret boundary", "ai", ai(format(incompatible)), false},
		{"prerelease", "ai", ai("1.2.0-beta.1"), false},
		{"build qualifier", "ai", ai("1.2.0+build"), false},
		{"leading zero", "ai", ai("1.02.0"), false},
		{"overflow", "ai", ai("1.2.18446744073709551616"), false},
		{"missing version", "ai", `{"name":"@earendil-works/pi-ai"}`, false},
		{"wrong dependency name", "ai", `{"name":"other","version":"1.2.0"}`, false},
		{"missing dependency name", "ai", `{"version":"1.2.0"}`, false},
		{"malformed dependency manifest", "ai", `{`, false},
		{"nonstring dependency version", "ai", `{"name":"@earendil-works/pi-ai","version":12}`, false},
		{"wrong CLI name", "cli", strings.Replace(cli, "pi-coding-agent", "other", 1), false},
		{"wrong CLI version", "cli", strings.Replace(cli, `"version":"`+piVersion+`"`, `"version":"`+format(incompatible)+`"`, 1), false},
		{"missing CLI name", "cli", strings.Replace(cli, `"name":"@earendil-works/pi-coding-agent",`, "", 1), false},
		{"missing CLI version", "cli", strings.Replace(cli, `"version":"`+piVersion+`",`, "", 1), false},
		{"missing declaration", "cli", strings.Replace(cli, `"@earendil-works/pi-ai":"^`+piVersion+`"`, "", 1), false},
		{"missing dependencies", "cli", `{"name":"@earendil-works/pi-coding-agent","version":"` + piVersion + `"}`, false},
		{"null dependencies", "cli", `{"name":"@earendil-works/pi-coding-agent","version":"` + piVersion + `","dependencies":null}`, false},
		{"wrong declaration name", "cli", strings.Replace(cli, "@earendil-works/pi-ai", "other", 1), false},
		{"wider declaration", "cli", strings.Replace(cli, "^"+piVersion, "*", 1), false},
		{"compound declaration", "cli", strings.Replace(cli, "^"+piVersion, "^"+piVersion+" || ^2.0.0", 1), false},
		{"different caret declaration", "cli", strings.Replace(cli, "^"+piVersion, "^"+format(incompatible), 1), false},
		{"exact declaration", "cli", strings.Replace(cli, "^"+piVersion, piVersion, 1), true},
		{"nonstring declaration", "cli", strings.Replace(cli, `"^`+piVersion+`"`, `12`, 1), false},
		{"null declaration", "cli", strings.Replace(cli, `"^`+piVersion+`"`, `null`, 1), false},
		{"malformed CLI manifest", "cli", `{`, false},
		{"CLI size boundary", "cli", cli + strings.Repeat(" ", (64<<10)-len(cli)), true},
		{"oversized CLI", "cli", cli + strings.Repeat(" ", (64<<10)+1-len(cli)), false},
		{"dependency size boundary", "ai", ai(piVersion) + strings.Repeat(" ", (64<<10)-len(ai(piVersion))), true},
		{"oversized dependency", "ai", ai(piVersion) + strings.Repeat(" ", (64<<10)+1-len(ai(piVersion))), false},
		{"missing CLI manifest", "cli", "", false},
		{"missing dependency manifest", "ai", "", false},
		{"unexpected CLI layout", "layout", "", false},
		{"resolved symlink layout", "symlink", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writePiModuleFixtures(t, root, piVersion)
			executable := filepath.Join(root, "dist", "bundle", "cli.js")
			if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, []byte("#!/bin/sh\necho '"+piVersion+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			switch test.target {
			case "cli", "ai":
				path := filepath.Join(root, "package.json")
				if test.target == "ai" {
					path = filepath.Join(root, "node_modules", "@earendil-works", "pi-ai", "package.json")
				}
				if test.content == "" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
					t.Fatal(err)
				}
			case "layout":
				wrong := filepath.Join(root, "cli.js")
				if err := os.Rename(executable, wrong); err != nil {
					t.Fatal(err)
				}
				executable = wrong
			case "symlink":
				link := filepath.Join(root, "pi")
				if err := os.Symlink(executable, link); err != nil {
					t.Fatal(err)
				}
				executable = link
			}
			if got := shippedPiModules(executable); got != test.want {
				t.Fatalf("shippedPiModules = %v, want %v", got, test.want)
			}
			opener := filepath.Join(root, "opener")
			if err := os.WriteFile(opener, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			env := launchEnvironment(context.Background(), "pi", executable, "/fixture/register.sock", nil, opener)
			if got := slices.Contains(env, "BROWSER="+opener) && slices.Contains(env, "NOISEDECK_LOGIN_SOCKET=/fixture/register.sock"); got != test.want {
				t.Fatalf("launch handoff admitted = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCodexVersionPrintingScriptCannotQualifyAsNativeOwner(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\necho 'codex-cli "+codexVersion+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if ShippedCodexExecutable(context.Background(), executable) != "" {
		t.Fatal("version-printing helper admitted as native Codex")
	}
	if ShippedCodexExecutable(context.Background(), "/bin/true") != "" {
		t.Fatal("unreviewed native executable admitted")
	}
}
