// Command upstream refreshes testdata/upstream from the upstream providers the
// description pins: the Terraform provider's schema, trimmed to the resource
// types the kinds use, and the CRDs of the Crossplane package for those
// kinds. The unit tests check every resource against them
// (sdktest.ValidateTerraform, sdktest.ValidateCrossplane).
//
//	go run ./tools/upstream        # or: make upstream
//
// It runs terraform (TF=tofu for OpenTofu) and crane (from PATH, or through
// go run when it isn't installed).
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hayone1/gitopsify-provider-grafana/internal/grafana"
	"github.com/hayone1/gitopsify/sdk"
	"sigs.k8s.io/yaml"
)

// craneModule runs crane when it isn't on PATH.
const craneModule = "github.com/google/go-containerregistry/cmd/crane@v0.20.6"

func main() {
	out := flag.String("out", "testdata/upstream", "directory to write terraform-schema.json and crds/ to")
	flag.Parse()
	d := grafana.New("dev").Describe()
	if err := run(d, *out); err != nil {
		fmt.Fprintln(os.Stderr, "upstream:", err)
		os.Exit(1)
	}
}

func run(d sdk.Description, out string) error {
	if d.Terraform == nil && d.Crossplane == nil {
		return errors.New("the description names no upstream provider")
	}
	if d.Terraform != nil && strings.HasPrefix(d.Terraform.Source, "example/") {
		return fmt.Errorf("%s is the template's made-up provider: name your tool's upstream providers in internal/provider/describe.go first", d.Terraform.Source)
	}
	var versions []string
	if d.Terraform != nil {
		v, err := terraformSchema(d, out)
		if err != nil {
			return fmt.Errorf("terraform: %w", err)
		}
		versions = append(versions, fmt.Sprintf("terraform %s %s (constraint %s)", d.Terraform.Source, v, d.Terraform.Version))
	}
	if d.Crossplane != nil {
		if err := crossplaneCRDs(d, filepath.Join(out, "crds")); err != nil {
			return fmt.Errorf("crossplane: %w", err)
		}
		versions = append(versions, "crossplane "+d.Crossplane.Package)
	}
	// What the test data was taken from: a version constraint can resolve to
	// a newer release next time.
	return os.WriteFile(filepath.Join(out, "VERSIONS"), []byte(strings.Join(versions, "\n")+"\n"), 0o644)
}

// terraformSchema writes terraform-schema.json: `terraform providers schema
// -json` for the pinned provider, with only the resource types the kinds use.
// It returns the version terraform chose.
func terraformSchema(d sdk.Description, out string) (string, error) {
	tf := os.Getenv("TF")
	if tf == "" {
		tf = "terraform"
	}
	dir, err := os.MkdirTemp("", "upstream-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	p := d.Terraform
	mainTF := fmt.Sprintf("terraform {\n  required_providers {\n    %s = {\n      source  = %q\n      version = %q\n    }\n  }\n}\n", p.Name, p.Source, p.Version)
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(mainTF), 0o600); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "downloading %s %s with %s\n", p.Source, p.Version, tf)
	if err := show(dir, tf, "init", "-input=false", "-no-color"); err != nil {
		return "", err
	}
	version := lockedVersion(filepath.Join(dir, ".terraform.lock.hcl"))
	raw, err := command(dir, tf, "providers", "schema", "-json")
	if err != nil {
		return "", err
	}
	var full struct {
		FormatVersion   string `json:"format_version"`
		ProviderSchemas map[string]struct {
			ResourceSchemas map[string]any `json:"resource_schemas"`
		} `json:"provider_schemas"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&full); err != nil {
		return "", fmt.Errorf("reading the schema: %w", err)
	}
	for addr, ps := range full.ProviderSchemas {
		if addr != p.Source && !strings.HasSuffix(addr, "/"+p.Source) {
			continue
		}
		kept := map[string]any{}
		for _, k := range d.Kinds {
			if k.Terraform == nil {
				continue
			}
			s, ok := ps.ResourceSchemas[k.Terraform.Type]
			if !ok {
				return "", fmt.Errorf("kind %s: %s has no resource %s", k.Name, p.Source, k.Terraform.Type)
			}
			kept[k.Terraform.Type] = s
		}
		trimmed := map[string]any{
			"format_version":   full.FormatVersion,
			"provider_schemas": map[string]any{addr: map[string]any{"resource_schemas": kept}},
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", " ")
		if err := enc.Encode(trimmed); err != nil {
			return "", err
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return "", err
		}
		target := filepath.Join(out, "terraform-schema.json")
		fmt.Fprintf(os.Stderr, "wrote %s (%s %s, %d resource types)\n", target, p.Source, version, len(kept))
		return version, os.WriteFile(target, buf.Bytes(), 0o644)
	}
	return "", fmt.Errorf("the schema has no provider %s", p.Source)
}

var lockVersion = regexp.MustCompile(`(?m)^\s*version\s*=\s*"([^"]+)"`)

// lockedVersion returns the provider version in a lock file terraform init
// wrote for one provider.
func lockedVersion(lockFile string) string {
	b, err := os.ReadFile(lockFile)
	if err != nil {
		return "(unknown version)"
	}
	if m := lockVersion.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return "(unknown version)"
}

// crossplaneCRDs writes the CRDs of the kinds' Crossplane types, taken from
// the package's package.yaml, to dir as <group>_<plural>.yaml (the names
// controller-gen gives them).
func crossplaneCRDs(d sdk.Description, dir string) error {
	pkg := d.Crossplane.Package
	want := map[string]string{} // group/version/Kind -> kind name
	for _, k := range d.Kinds {
		if k.Crossplane != nil {
			want[k.Crossplane.APIVersion+"/"+k.Crossplane.Kind] = k.Name
		}
	}
	fmt.Fprintf(os.Stderr, "reading the CRDs of %s\n", pkg)
	pkgYAML, err := packageYAML(pkg)
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	found := map[string]bool{}
	for _, doc := range splitYAML(pkgYAML) {
		var crd struct {
			Kind string `json:"kind"`
			Spec struct {
				Group string `json:"group"`
				Names struct {
					Kind   string `json:"kind"`
					Plural string `json:"plural"`
				} `json:"names"`
				Versions []struct {
					Name string `json:"name"`
				} `json:"versions"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(doc, &crd); err != nil || crd.Kind != "CustomResourceDefinition" {
			continue
		}
		for _, v := range crd.Spec.Versions {
			gvk := crd.Spec.Group + "/" + v.Name + "/" + crd.Spec.Names.Kind
			if _, ok := want[gvk]; ok {
				found[gvk] = true
				files[crd.Spec.Group+"_"+crd.Spec.Names.Plural+".yaml"] = doc
			}
		}
	}
	var missing []string
	for gvk, kind := range want {
		if !found[gvk] {
			missing = append(missing, fmt.Sprintf("kind %s: %s has no CRD for %s", kind, pkg, gvk))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return errors.New(strings.Join(missing, "; "))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	for name, doc := range files {
		if err := os.WriteFile(filepath.Join(dir, name), append([]byte("---\n"), doc...), 0o644); err != nil {
			return err
		}
	}
	// Then the CRDs of kinds that are gone.
	for _, f := range old {
		if _, ok := files[filepath.Base(f)]; !ok {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(os.Stderr, "wrote %d CRDs to %s\n", len(files), dir)
	return nil
}

// packageYAML returns package.yaml from a Crossplane package: the layer the
// xpkg format annotates io.crossplane.xpkg: base.
func packageYAML(pkg string) ([]byte, error) {
	raw, err := crane("manifest", "--platform", "linux/amd64", pkg)
	if err != nil {
		return nil, err
	}
	var m struct {
		Layers []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("reading the manifest of %s: %w", pkg, err)
	}
	layer := ""
	for _, l := range m.Layers {
		if l.Annotations["io.crossplane.xpkg"] == "base" {
			layer = l.Digest
			break
		}
	}
	if layer == "" && len(m.Layers) == 1 {
		layer = m.Layers[0].Digest // older packages: one layer, no annotation
	}
	if layer == "" {
		return nil, fmt.Errorf("%s has no layer annotated io.crossplane.xpkg: base; is it a Crossplane package?", pkg)
	}
	blob, err := crane("blob", repository(pkg)+"@"+layer)
	if err != nil {
		return nil, err
	}
	return fileInLayer(blob, "package.yaml")
}

// repository strips the digest and the tag from an image reference.
func repository(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}

func fileInLayer(blob []byte, name string) ([]byte, error) {
	var r io.Reader = bytes.NewReader(blob)
	if gz, err := gzip.NewReader(bytes.NewReader(blob)); err == nil {
		r = gz
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the package layer has no %s", name)
		}
		if err != nil {
			return nil, fmt.Errorf("reading the package layer: %w", err)
		}
		if strings.TrimPrefix(h.Name, "./") == name {
			return io.ReadAll(tr)
		}
	}
}

var docSeparator = regexp.MustCompile(`(?m)^---[ \t]*\r?\n`)

// splitYAML splits a multi-document YAML file on its --- lines.
func splitYAML(b []byte) [][]byte {
	var docs [][]byte
	for _, d := range docSeparator.Split(string(b), -1) {
		d = strings.TrimLeft(d, "\r\n")
		if strings.TrimSpace(d) == "" {
			continue
		}
		if !strings.HasSuffix(d, "\n") {
			d += "\n"
		}
		docs = append(docs, []byte(d))
	}
	return docs
}

func crane(args ...string) ([]byte, error) {
	if _, err := exec.LookPath("crane"); err == nil {
		return command("", "crane", args...)
	}
	return command("", "go", append([]string{"run", craneModule}, args...)...)
}

// command runs a program and returns its stdout; its stderr goes to ours.
func command(dir, name string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	err := execute(dir, &out, name, args...)
	return out.Bytes(), err
}

// show runs a program whose output is progress (terraform init).
func show(dir, name string, args ...string) error {
	return execute(dir, os.Stderr, name, args...)
}

func execute(dir string, stdout io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
