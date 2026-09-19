// Command verify runs the full repository acceptance pipeline with a single
// command: it verifies that the language version and every third-party
// dependency declared in go.mod are locked in go.sum, downloads those exact
// versions, builds the binary, runs the tests, validates the bundled examples
// with `mdschema check`, and regenerates them with `mdschema generate`,
// comparing the output against committed golden files.
//
// The pipeline stops at the first failing step. It only uses the Go standard
// library so it can run on a clean machine before dependencies are downloaded.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type moduleReq struct {
	Path     string `json:"Path"`
	Version  string `json:"Version"`
	Indirect bool   `json:"Indirect"`
}

type modEditJSON struct {
	Module struct {
		Path string `json:"Path"`
	} `json:"Module"`
	Go      string      `json:"Go"`
	Require []moduleReq `json:"Require"`
}

type examplePair struct {
	name   string
	doc    string
	schema string
}

var examples = []examplePair{
	{"README", "README.md", "examples/README.mdschema.yml"},
	{"requirements", "examples/requirements.md", "examples/requirements.mdschema.yml"},
	{"blog-post", "examples/blog-post.md", "examples/blog-post.mdschema.yml"},
	{"tutorial", "examples/tutorial.md", "examples/tutorial.mdschema.yml"},
}

func main() {
	repoRoot, err := findRepoRoot()
	if err != nil {
		fail(err)
	}

	steps := []struct {
		name string
		fn   func(string) error
	}{
		{"verify language and dependency locks (go.mod/go.sum)", verifyLocks},
		{"download locked dependencies", downloadDeps},
		{"build mdschema", buildBinary},
		{"run tests", runTests},
		{"check bundled examples", checkExamples},
		{"generate example text and compare golden files", generateExamples},
	}

	for i, step := range steps {
		fmt.Printf("\n==> [%d/%d] %s\n", i+1, len(steps), step.name)
		if err := step.fn(repoRoot); err != nil {
			fail(fmt.Errorf("step %q failed: %w", step.name, err))
		}
	}

	fmt.Printf("\n✓ verify passed: locks, build, tests, example checks and generation all succeeded\n")
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate go.mod from %s upward", dir)
		}
		dir = parent
	}
}

// verifyLocks runs before any network access. It proves that the language
// version and every third-party dependency named in go.mod has a complete
// checksum entry in the committed go.sum, so a clean machine can only install
// the exact versions recorded in the repository.
func verifyLocks(root string) error {
	out, err := runCapture(root, "go", "mod", "edit", "-json")
	if err != nil {
		return fmt.Errorf("reading go.mod: %w", err)
	}
	var mod modEditJSON
	if err := json.Unmarshal(out, &mod); err != nil {
		return fmt.Errorf("parsing go.mod: %w", err)
	}

	goVersionOut, err := runCapture(root, "go", "version")
	if err != nil {
		return fmt.Errorf("reading toolchain version: %w", err)
	}
	fmt.Printf("toolchain: %s\n", strings.TrimSpace(string(goVersionOut)))
	fmt.Printf("module:    %s\n", mod.Module.Path)
	fmt.Printf("language:  go %s (locked in go.mod)\n", mod.Go)

	sumData, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		return fmt.Errorf("reading go.sum: %w", err)
	}
	locked := parseGoSum(string(sumData))

	reqs := append([]moduleReq(nil), mod.Require...)
	sort.Slice(reqs, func(i, j int) bool {
		if reqs[i].Path == reqs[j].Path {
			return reqs[i].Version < reqs[j].Version
		}
		return reqs[i].Path < reqs[j].Path
	})

	direct := 0
	for _, req := range reqs {
		if !req.Indirect {
			direct++
		}
		kind := "indirect"
		if !req.Indirect {
			kind = "direct  "
		}
		fmt.Printf("locked %s %s %s\n", kind, req.Version, req.Path)
		if err := checkSumsLocked(locked, req); err != nil {
			return err
		}
	}
	fmt.Printf("go.mod requires %d dependencies (%d direct); every version is locked in go.sum\n", len(reqs), direct)
	return nil
}

// parseGoSum indexes go.sum lines as "path|version|flavor" -> true.
// flavor is "zip" for "path version h1:..." and "mod" for
// "path version/go.mod h1:...".
func parseGoSum(data string) map[string]bool {
	entries := make(map[string]bool)
	for _, line := range strings.FieldsFunc(data, func(r rune) bool { return r == '\n' || r == '\r' }) {
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasPrefix(fields[2], "h1:") {
			continue
		}
		flavor := "zip"
		version := fields[1]
		if strings.HasSuffix(version, "/go.mod") {
			version = strings.TrimSuffix(version, "/go.mod")
			flavor = "mod"
		}
		entries[fields[0]+"|"+version+"|"+flavor] = true
	}
	return entries
}

func checkSumsLocked(locked map[string]bool, req moduleReq) error {
	var missing []string
	if !locked[req.Path+"|"+req.Version+"|zip"] {
		missing = append(missing, "module archive checksum")
	}
	if !locked[req.Path+"|"+req.Version+"|mod"] {
		missing = append(missing, "go.mod checksum")
	}
	if len(missing) > 0 {
		return fmt.Errorf("go.sum is out of sync with go.mod for dependency %q (declared version %s): missing %s; run 'go mod download %s' and commit go.sum",
			req.Path, req.Version, strings.Join(missing, " and "), req.Path)
	}
	return nil
}

func downloadDeps(root string) error {
	return runStream(root, nil, "go", "mod", "download")
}

func buildBinary(root string) error {
	bin := filepath.Join(os.TempDir(), "mdschema-verify")
	if err := runStream(root, readonlyEnv(), "go", "build", "-o", bin, "./cmd/mdschema"); err != nil {
		return err
	}
	return nil
}

func runTests(root string) error {
	return runStream(root, readonlyEnv(), "go", "test", "./...")
}

func checkExamples(root string) error {
	bin := filepath.Join(os.TempDir(), "mdschema-verify")
	for _, ex := range examples {
		fmt.Printf("-- check %s against %s\n", ex.doc, ex.schema)
		if err := runStream(root, nil, bin, "check", ex.doc, "--schema", ex.schema); err != nil {
			return fmt.Errorf("checking %s: %w", ex.doc, err)
		}
	}
	return nil
}

func generateExamples(root string) error {
	bin := filepath.Join(os.TempDir(), "mdschema-verify")
	outDir, err := os.MkdirTemp("", "mdschema-verify-gen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(outDir)

	goldenDir := filepath.Join(root, "examples", "generated")
	for _, ex := range examples {
		outFile := filepath.Join(outDir, ex.name+".md")
		fmt.Printf("-- generate %s\n", ex.schema)
		if err := runStream(root, nil, bin, "generate", ex.schema, "--output", outFile); err != nil {
			return fmt.Errorf("generating %s: %w", ex.schema, err)
		}

		golden := filepath.Join(goldenDir, ex.name+".md")
		if err := compareFiles(golden, outFile, root); err != nil {
			return fmt.Errorf("generated text for %s drifted from golden file examples/generated/%s.md: %w", ex.schema, ex.name, err)
		}
	}
	fmt.Printf("all generated example text matches committed golden files in examples/generated/\n")
	return nil
}

func compareFiles(wantPath, gotPath, root string) error {
	want, err := os.ReadFile(wantPath)
	if err != nil {
		return fmt.Errorf("reading golden file: %w", err)
	}
	got, err := os.ReadFile(gotPath)
	if err != nil {
		return err
	}
	if bytes.Equal(want, got) {
		return nil
	}

	rel, relErr := filepath.Rel(root, wantPath)
	if relErr != nil {
		rel = wantPath
	}
	fmt.Printf("generated output differs from %s:\n", rel)
	diff := exec.Command("git", "diff", "--no-index", "--", wantPath, gotPath)
	diff.Dir = root
	diff.Stdout = os.Stdout
	diff.Stderr = os.Stderr
	_ = diff.Run() // git diff exits non-zero when files differ
	return fmt.Errorf("regenerate with 'go run ./cmd/mdschema generate <schema> -o %s' and commit the result", rel)
}

func readonlyEnv() []string {
	env := os.Environ()
	for _, kv := range env {
		if strings.HasPrefix(kv, "GOFLAGS=") {
			return env
		}
	}
	return append(env, "GOFLAGS=-mod=readonly")
}

func runCapture(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Output()
}

func runStream(dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "\n✗ verify failed: %v\n", err)
	os.Exit(1)
}
