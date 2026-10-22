package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeckSkillsInstaller(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, "-B", "-m", "unittest", "-v", "test_install_skills.py")
	command.Dir = "skills"
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Deck skills installer tests failed: %v\n%s", err, output)
	}
}

// Both start-up scripts install the Deck skills, so they reach every
// harness whether or not the memory overlay is on.
func TestImageShipsAndStartupInstallsDeckSkills(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"COPY deploy/skills/ /opt/noisedeck/skills/",
		"COPY deploy/skills/install_skills.py /usr/local/lib/noisedeck/install_skills.py",
	} {
		if !strings.Contains(string(dockerfile), want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	const install = "/usr/bin/python3 /usr/local/lib/noisedeck/install_skills.py"
	for _, path := range []string{
		filepath.Join("provider-hook", "noisedeck-bootstrap"),
		filepath.Join("mempalace", "noisedeck-bootstrap"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		at := strings.Index(body, install)
		serve := strings.Index(body, "exec /usr/local/bin/noise deck serve")
		if at < 0 || serve < 0 || at > serve {
			t.Errorf("%s does not install the Deck skills before serve: install@%d serve@%d", path, at, serve)
		}
	}
}
