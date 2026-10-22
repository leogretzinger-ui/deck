package supervisor

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBubblewrapExecutablePinIsClosedPerTarget(t *testing.T) {
	want := map[string]string{
		"linux/amd64": "7710b19d89bedbdd3b1dd8ac46643f188f8fdc92917fe3093abd827eb783cf7f",
	}
	if !reflect.DeepEqual(pinnedBubblewrapExecutableSHA256, want) {
		t.Fatalf("executable pin map = %v, want %v", pinnedBubblewrapExecutableSHA256, want)
	}
	for target, digest := range want {
		if digest == PinnedBubblewrapArchiveSHA256 {
			t.Fatalf("%s pin equals the release archive digest", target)
		}
	}
}

func TestResolveBubblewrapExecutableAcceptsClosedTargets(t *testing.T) {
	for _, target := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "7710b19d89bedbdd3b1dd8ac46643f188f8fdc92917fe3093abd827eb783cf7f"},
	} {
		got, err := ResolveBubblewrapExecutableSHA256(target.goos, target.goarch)
		if err != nil {
			t.Fatalf("ResolveBubblewrapExecutableSHA256(%s,%s): %v", target.goos, target.goarch, err)
		}
		if got != target.want {
			t.Fatalf("pin(%s/%s) = %s, want %s", target.goos, target.goarch, got, target.want)
		}
	}
}

func TestResolveBubblewrapExecutableRejectsUnsupportedTargets(t *testing.T) {
	for _, target := range [][2]string{
		{"linux", "386"},
		{"linux", "arm64"},
		{"linux", "riscv64"},
		{"darwin", "arm64"},
		{"darwin", "amd64"},
		{"windows", "amd64"},
		{"freebsd", "arm64"},
		{"", ""},
		{"linux", ""},
		{"", "amd64"},
		{"linux", "AMD64"},
		{"linux", "amd64 "},
		{"linux/amd64", ""},
	} {
		digest, err := ResolveBubblewrapExecutableSHA256(target[0], target[1])
		if !errors.Is(err, ErrUnsupportedBubblewrapTarget) {
			t.Fatalf("ResolveBubblewrapExecutableSHA256(%q,%q) error = %v, want %v", target[0], target[1], err, ErrUnsupportedBubblewrapTarget)
		}
		if err == nil || err.Error() != ErrUnsupportedBubblewrapTarget.Error() {
			t.Fatalf("ResolveBubblewrapExecutableSHA256(%q,%q) error = %v, want exactly the fixed sentinel %q", target[0], target[1], err, ErrUnsupportedBubblewrapTarget)
		}
		if digest != "" {
			t.Fatalf("ResolveBubblewrapExecutableSHA256(%q,%q) digest = %q, want empty", target[0], target[1], digest)
		}
	}
}

// TestResolveBubblewrapUnsupportedErrorCarriesNoTargetContent proves the
// unsupported-target error is the fixed content-free sentinel: hostile
// GOOS/GOARCH strings never reach the error text, on either the resolve
// path or the verify path that wraps it.
func TestResolveBubblewrapUnsupportedErrorCarriesNoTargetContent(t *testing.T) {
	hostile := [][2]string{
		{"linux", "amd64; touch /tmp/pwned"},
		{"linux\nrm -rf /", "amd64"},
		{"$(curl http://169.254.169.254/latest/meta-data/)", "amd64"},
		{"linux", "%s%s%s"},
		{"../../secrets", "amd64"},
		{"linux\u0000inject", "amd64"},
	}
	for _, target := range hostile {
		digest, err := ResolveBubblewrapExecutableSHA256(target[0], target[1])
		if digest != "" {
			t.Fatalf("ResolveBubblewrapExecutableSHA256(%q,%q) digest = %q, want empty", target[0], target[1], digest)
		}
		if err == nil || err.Error() != ErrUnsupportedBubblewrapTarget.Error() {
			t.Fatalf("ResolveBubblewrapExecutableSHA256(%q,%q) error = %v, want exactly the fixed sentinel %q", target[0], target[1], err, ErrUnsupportedBubblewrapTarget)
		}
		for _, input := range target {
			if strings.Contains(err.Error(), input) {
				t.Fatalf("unsupported-target error %q contains hostile input %q", err, input)
			}
		}
	}
	err := VerifyBubblewrapExecutableSHA256("linux\nshutdown", "amd64) ; poweroff", strings.Repeat("a", 64))
	if err == nil || err.Error() != ErrUnsupportedBubblewrapTarget.Error() {
		t.Fatalf("VerifyBubblewrapExecutableSHA256 unsupported-target error = %v, want exactly the fixed sentinel %q", err, ErrUnsupportedBubblewrapTarget)
	}
}

func TestVerifyBubblewrapExecutableRequiresExactPin(t *testing.T) {
	amd64, err := ResolveBubblewrapExecutableSHA256("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}

	if err := VerifyBubblewrapExecutableSHA256("linux", "amd64", amd64); err != nil {
		t.Fatalf("exact pin rejected: %v", err)
	}

	tests := []struct {
		name     string
		goos     string
		goarch   string
		observed string
		want     error
	}{
		{"well formed foreign digest", "linux", "amd64", strings.Repeat("7", 64), ErrBubblewrapExecutableMismatch},
		{"archive digest substitution", "linux", "amd64", PinnedBubblewrapArchiveSHA256, ErrBubblewrapExecutableMismatch},
		{"malformed digest", "linux", "amd64", "not-a-digest", ErrBubblewrapExecutableMismatch},
		{"empty digest", "linux", "amd64", "", ErrBubblewrapExecutableMismatch},
		{"uppercase digest", "linux", "amd64", strings.ToUpper(amd64), ErrBubblewrapExecutableMismatch},
		{"short digest", "linux", "amd64", amd64[:63], ErrBubblewrapExecutableMismatch},
		{"unknown target", "linux", "riscv64", amd64, ErrUnsupportedBubblewrapTarget},
		{"non linux target", "darwin", "amd64", amd64, ErrUnsupportedBubblewrapTarget},
	}
	for _, test := range tests {
		err := VerifyBubblewrapExecutableSHA256(test.goos, test.goarch, test.observed)
		if !errors.Is(err, test.want) {
			t.Fatalf("%s: error = %v, want %v", test.name, err, test.want)
		}
	}
}

func TestCurrentBubblewrapExecutablePinMatchesRuntimeTarget(t *testing.T) {
	digest, err := CurrentBubblewrapExecutableSHA256()
	if err != nil {
		// Outside the closed pin map no pin exists to match; the runtime
		// lookup must fail closed with the fixed sentinel and no digest.
		if !errors.Is(err, ErrUnsupportedBubblewrapTarget) || err.Error() != ErrUnsupportedBubblewrapTarget.Error() {
			t.Fatalf("CurrentBubblewrapExecutableSHA256 error = %v, want exactly the fixed sentinel %v", err, ErrUnsupportedBubblewrapTarget)
		}
		if digest != "" {
			t.Fatalf("current pin = %q, want empty on an unsupported target", digest)
		}
		return
	}
	if digest == "" || !isLowerSHA256(digest) {
		t.Fatalf("current pin = %q, want a lowercase sha256", digest)
	}
	if digest == PinnedBubblewrapArchiveSHA256 {
		t.Fatal("current pin equals the release archive digest")
	}
}

// TestBubblewrapPinSurfaceExposesNoCallerControlledIdentity proves the
// executable identity is compiled in, not caller input: the only exported
// declarations are the closed resolver, the equality check, and their two
// sentinel errors, and the pin map itself is unexported with exactly the
// one closed target.
func TestBubblewrapPinSurfaceExposesNoCallerControlledIdentity(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("bubblewrap.go"))
	if err != nil {
		t.Fatalf("read bubblewrap.go: %v", err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "bubblewrap.go", body, 0)
	if err != nil {
		t.Fatalf("parse bubblewrap.go: %v", err)
	}

	exported := map[string]bool{}
	var mapNames []string
	var mapEntries int
	for _, decl := range parsed.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Recv == nil && ast.IsExported(decl.Name.Name) {
				exported[decl.Name.Name] = true
			}
			for _, param := range decl.Type.Params.List {
				if _, ok := param.Type.(*ast.MapType); ok {
					t.Fatalf("exported function %s accepts a map parameter", decl.Name.Name)
				}
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					if ast.IsExported(name.Name) {
						exported[name.Name] = true
					}
					if decl.Tok == token.VAR && strings.HasPrefix(name.Name, "pinnedBubblewrap") {
						mapNames = append(mapNames, name.Name)
						if composite, ok := value.Values[0].(*ast.CompositeLit); ok {
							mapEntries = len(composite.Elts)
						}
					}
				}
			}
		}
	}

	wantExported := map[string]bool{
		"ResolveBubblewrapExecutableSHA256": true,
		"VerifyBubblewrapExecutableSHA256":  true,
		"CurrentBubblewrapExecutableSHA256": true,
		"ErrUnsupportedBubblewrapTarget":    true,
		"ErrBubblewrapExecutableMismatch":   true,
	}
	if !reflect.DeepEqual(exported, wantExported) {
		t.Fatalf("exported surface = %v, want %v", exported, wantExported)
	}
	if len(mapNames) != 1 || ast.IsExported(mapNames[0]) {
		t.Fatalf("pin map declarations = %v, want one unexported map", mapNames)
	}
	if mapEntries != 1 {
		t.Fatalf("pin map has %d entries, want exactly the one closed target", mapEntries)
	}
}
