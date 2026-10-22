package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSidecarDNSQualificationFailsClosedUnderOptimization(t *testing.T) {
	directory := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("getenforce", "#!/bin/sh\necho Enforcing\n")
	write("docker", `#!/usr/bin/env python3
import json, os, sys
args=sys.argv[1:]
fault=os.environ['DNS_TEST_FAULT']
if args[0]=='inspect':
    role=args[1].rsplit('-',1)[1]
    labels={'app':'c1,c2','browser':'c3,c4','connector':'c5,c6','control':'c7,c8','outsider':'c9,c10'}
    level='c1,c2' if fault=='labels' else labels[role]
    health='unhealthy' if fault=='health' else 'healthy'
    print(json.dumps([{'ProcessLabel':'system_u:system_r:container_t:s0:'+level,'State':{'Health':{'Status':health}}}]))
elif args[0]=='exec':
    role=args[1].rsplit('-',1)[1]
    if role=='control' and fault!='control':
        print('EACCES: permission denied /etc/resolv.conf',file=sys.stderr)
        sys.exit(1)
    if role=='outsider' and fault!='private':
        print('cat: /private-marker: Permission denied',file=sys.stderr)
        sys.exit(1)
    if args[2]=='test':
        sys.exit(1)
`)
	for fault, message := range map[string]string{
		"labels": "distinct process labels", "control": "Control did not reproduce",
		"health": "App health check failed", "private": "Outsider private-marker read was not refused",
	} {
		for _, optimized := range []bool{false, true} {
			name := fault
			if optimized {
				name += "-optimized"
			}
			t.Run(name, func(t *testing.T) {
				args := []string{}
				if optimized {
					args = append(args, "-O")
				}
				args = append(args, "-c", `import ast,os,pathlib,runpy
source=pathlib.Path('sidecar-dns-acceptance.py').read_text()
if any(isinstance(node,ast.Assert) for node in ast.walk(ast.parse(source))):
    raise SystemExit('Qualification must not use removable assertions')
module=runpy.run_path('sidecar-dns-acceptance.py')
module['time'].sleep=lambda _:None
temporary=module['tempfile'].TemporaryDirectory
module['tempfile'].TemporaryDirectory=lambda **kwargs:temporary(prefix=kwargs['prefix'],dir=os.environ['DNS_TEST_TMP'])
module['main']()
`)
				command := exec.Command("python3", args...)
				command.Env = []string{"PATH=" + directory + ":" + os.Getenv("PATH"), "DNS_TEST_FAULT=" + fault, "DNS_TEST_TMP=" + t.TempDir()}
				out, err := command.CombinedOutput()
				if err == nil || !strings.Contains(string(out), message) || strings.Contains(string(out), `"generation"`) {
					t.Fatalf("fault %s optimized=%v was not rejected before success output: %v\n%s", fault, optimized, err, out)
				}
			})
		}
	}
}
