package main

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepo(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// versioned are the build files scripts/set-version.sh writes the release version into.
var versioned = []string{
	"build/config.yml",
	"build/darwin/Info.plist",
	"build/darwin/Info.dev.plist",
	"build/windows/info.json",
	"build/windows/kolo-share.exe.manifest",
	"build/windows/msix/app_manifest.xml",
	"build/windows/msix/template.xml",
	"build/windows/nsis/wails_tools.nsh",
	"build/windows/nsis/project.nsi",
	"build/linux/nfpm/nfpm.yaml",
}

func TestBuildFilesCarryTheDevelopmentVersion(t *testing.T) {
	// MinVersion and MaxVersionTested of the MSIX files are Windows builds, not versions of the app.
	windowsBuilds := regexp.MustCompile(`(?:MinVersion|MaxVersionTested)="[^"]*"`)
	hardcoded := regexp.MustCompile(`\b0\.(?:[1-9]|[0-9]{2,})[0-9]*\.[0-9]+\b`)
	for _, f := range versioned {
		text := windowsBuilds.ReplaceAllString(readRepo(t, f), "")
		if !strings.Contains(text, "0.0.0") {
			t.Errorf("%s has no development version 0.0.0", f)
		}
		if m := hardcoded.FindString(text); m != "" {
			t.Errorf("%s has the hard-coded version %s: the release script writes it", f, m)
		}
	}
	if m := readRepo(t, "build/windows/kolo-share.exe.manifest"); !strings.Contains(m, `name="dev.ciranda.koloshare" version="0.0.0.0"`) {
		t.Error("the Windows manifest does not carry the development version")
	}
	if version != "dev" {
		t.Errorf("the version of a build without -ldflags is %q, want dev", version)
	}
}

func TestSetVersionWritesEveryBuildFile(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	root := t.TempDir()
	for _, f := range append([]string{"scripts/set-version.sh"}, versioned...) {
		dst := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(readRepo(t, f)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(arg string) ([]byte, error) {
		cmd := exec.Command(bash, filepath.Join(root, "scripts", "set-version.sh"), arg)
		cmd.Dir = t.TempDir()
		return cmd.CombinedOutput()
	}
	if out, err := run("v1.2.3-4-gabc1234"); err != nil {
		t.Fatalf("set-version: %v\n%s", err, out)
	}
	for _, f := range versioned {
		b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		text := string(b)
		// The manifest also names the Common-Controls assembly 6.0.0.0, checked below.
		if !strings.Contains(text, "1.2.3") || strings.Contains(text, "0.0.0") && !strings.HasSuffix(f, ".exe.manifest") {
			t.Errorf("%s after set-version 1.2.3:\n%s", f, text)
		}
	}
	plist, _ := os.ReadFile(filepath.Join(root, "build/darwin/Info.plist"))
	if strings.Count(string(plist), "<string>1.2.3</string>") != 2 {
		t.Errorf("Info.plist wants the version in CFBundleVersion and CFBundleShortVersionString:\n%s", plist)
	}
	manifest, _ := os.ReadFile(filepath.Join(root, "build/windows/kolo-share.exe.manifest"))
	if !strings.Contains(string(manifest), `name="dev.ciranda.koloshare" version="1.2.3.0"`) {
		t.Errorf("the Windows manifest wants four parts:\n%s", manifest)
	}
	for _, bad := range []string{"dev", "", "v1.2", "release"} {
		if out, err := run(bad); err == nil {
			t.Errorf("set-version %q succeeded:\n%s", bad, out)
		}
	}
}

// The build tasks run set-version once per architecture and package, so a second run with the same version must
// succeed and change nothing, while a file whose version line is gone still fails.
func TestSetVersionRunsTwice(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	root := t.TempDir()
	for _, f := range append([]string{"scripts/set-version.sh"}, versioned...) {
		dst := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(readRepo(t, f)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func() ([]byte, error) {
		return exec.Command(bash, filepath.Join(root, "scripts", "set-version.sh"), "0.1.15").CombinedOutput()
	}
	files := func() map[string]string {
		m := map[string]string{}
		for _, f := range versioned {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
			if err != nil {
				t.Fatal(err)
			}
			m[f] = string(b)
		}
		return m
	}
	if out, err := run(); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	first := files()
	if out, err := run(); err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if !maps.Equal(first, files()) {
		t.Error("a second run with the same version changed the files")
	}
	cfg := filepath.Join(root, "build", "config.yml")
	if err := os.WriteFile(cfg, []byte(strings.Replace(first["build/config.yml"], "  version:", "  release:", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil || !strings.Contains(string(out), "no version to replace in build/config.yml") {
		t.Errorf("a config.yml without a version line: %v\n%s", err, out)
	}
}

// taskVersion runs the Taskfile command behind name the way Task does (errexit) in a folder with no git
// repository, as in the release copy, with KOLO_VERSION set to env.
func taskVersion(t *testing.T, name, env string) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	m := regexp.MustCompile(`(?m)^  ` + name + `:\n    sh: '(.*)'$`).FindStringSubmatch(readRepo(t, "Taskfile.yml"))
	if m == nil {
		t.Fatalf("the Taskfile has no %s command", name)
	}
	dir := t.TempDir()
	cmd := exec.Command(bash, "-ec", m[1])
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "KOLO_VERSION="+env, "GIT_DIR="+filepath.Join(dir, "none"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s with KOLO_VERSION=%q: %v", name, env, err)
	}
	return strings.TrimSpace(string(out))
}

func TestTaskfileVersionWithoutGit(t *testing.T) {
	for env, want := range map[string]string{"v0.1.15": "0.1.15", "0.1.15": "0.1.15", "": "dev"} {
		if got := taskVersion(t, "APP_VERSION", env); got != want {
			t.Errorf("APP_VERSION with KOLO_VERSION=%q is %q, want %q", env, got, want)
		}
	}
}

// The release script checks each binary for the version with its carries function.
func TestReleaseFindsTheVersionInABinary(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	fn := regexp.MustCompile(`(?m)^carries\(\) \{.*\}$`).FindString(readRepo(t, "scripts/release-local.sh"))
	if fn == "" {
		t.Fatal("scripts/release-local.sh has no carries function")
	}
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		ver, body string
		want      bool
	}{
		"injected":      {"0.1.15", "\x00\x00go1.26\x00\x000.1.15\x00\x00", true},
		"module only":   {"0.1.15", "github.com/ciranda-dev/kolo-dht\tv0.1.15\th1:abc", false},
		"other version": {"0.1.15", "\x00\x000.1.14\x00", false},
		"dirty":         {"0.1.15-dirty", "\x000.1.15-dirty\x00", true},
	} {
		f := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(f, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		err := exec.Command(bash, "-c", "set -euo pipefail; ver=$1; "+fn+"; carries \"$2\"", "bash", tc.ver, f).Run()
		if got := err == nil; got != tc.want {
			t.Errorf("%s: carries = %v, want %v", name, got, tc.want)
		}
	}
}

func TestVersionIsInjectedByEveryBuild(t *testing.T) {
	flag := "-X main.version={{.APP_VERSION}}"
	for _, f := range []string{"build/darwin/Taskfile.yml", "build/linux/Taskfile.yml", "build/windows/Taskfile.yml"} {
		if !strings.Contains(readRepo(t, f), flag) {
			t.Errorf("%s does not inject the version", f)
		}
	}
	rel := readRepo(t, "scripts/release-local.sh")
	for _, want := range []string{`-X main.version=$ver`, `APP_VERSION="$ver"`, `scripts/set-version.sh "$ver"`, `KOLO_VERSION=$ver`, `stash create`} {
		if !strings.Contains(rel, want) {
			t.Errorf("scripts/release-local.sh lacks %s", want)
		}
	}
	if !strings.Contains(readRepo(t, "Taskfile.yml"), "APP_VERSION:") {
		t.Error("the Taskfile has no APP_VERSION")
	}
}

func TestInstallerMetadata(t *testing.T) {
	nfpm := readRepo(t, "build/linux/nfpm/nfpm.yaml")
	for _, want := range []string{`maintainer: "Ciranda <hello@ciranda.dev>"`, `homepage: "https://kolo.ciranda.dev"`, `license: "Apache-2.0"`} {
		if !strings.Contains(nfpm, want) {
			t.Errorf("nfpm.yaml lacks %s", want)
		}
	}
	for _, bad := range []string{"GIT_COMMITTER", "wails.io"} {
		if strings.Contains(nfpm, bad) {
			t.Errorf("nfpm.yaml still has %s", bad)
		}
	}
}

func TestUninstallerNeverRemovesAFolderBlindly(t *testing.T) {
	nsi := readRepo(t, "build/windows/nsis/project.nsi")
	if regexp.MustCompile(`(?i)RMDir\s+/r\s+"?\$INSTDIR`).MatchString(nsi) {
		t.Error("the uninstaller removes the install folder recursively")
	}
	uninstall := nsi[strings.Index(nsi, `Section "uninstall"`):]
	del := strings.Index(uninstall, `Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"`)
	rm := strings.Index(uninstall, `RMDir "$INSTDIR"`)
	gone := strings.Index(uninstall, "wails.deleteUninstaller")
	if del < 0 || rm < 0 || gone < 0 || !(del < gone && gone < rm) {
		t.Errorf("the uninstaller must delete the executable, the uninstaller, then RMDir the folder: %d %d %d", del, gone, rm)
	}
}

func TestAppImageBuildPinsAndChecksLinuxdeploy(t *testing.T) {
	sh := readRepo(t, "build/linux/appimage/build.sh")
	if strings.Contains(sh, "continuous") {
		t.Error("linuxdeploy is taken from the moving continuous release")
	}
	if !regexp.MustCompile(`LINUXDEPLOY_RELEASE="1-alpha-\d{8}-\d+"`).MatchString(sh) {
		t.Error("linuxdeploy is not pinned to a release")
	}
	if n := len(regexp.MustCompile(`LINUXDEPLOY_SHA256="[0-9a-f]{64}"`).FindAllString(sh, -1)); n != 2 {
		t.Errorf("%d checksums, want one per architecture", n)
	}
	download, check, run := strings.Index(sh, "wget"), strings.Index(sh, "sha256sum -c"), strings.Index(sh, `"./${LINUXDEPLOY}" --appdir`)
	if download < 0 || check < download || run < check {
		t.Errorf("the checksum must be checked after the download and before the run: %d %d %d", download, check, run)
	}
}
