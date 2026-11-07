package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"strings"
	"testing"
)

func TestRepository(t *testing.T) {
	for ref, want := range map[string]string{
		"xpkg.upbound.io/grafana/provider-grafana:v2.17.0":   "xpkg.upbound.io/grafana/provider-grafana",
		"localhost:5000/acme/provider-x:v1":                  "localhost:5000/acme/provider-x",
		"xpkg.upbound.io/acme/provider-x@sha256:abc":         "xpkg.upbound.io/acme/provider-x",
		"localhost:5000/acme/provider-x":                     "localhost:5000/acme/provider-x",
		"xpkg.upbound.io/acme/provider-x:v1@sha256:0123abcd": "xpkg.upbound.io/acme/provider-x",
	} {
		if got := repository(ref); got != want {
			t.Errorf("repository(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestSplitYAML(t *testing.T) {
	docs := splitYAML([]byte("# meta\napiVersion: meta.pkg.crossplane.io/v1\n---\nkind: A\ndescription: |\n  ---\n  not a separator\n--- \r\nkind: B\n---\n"))
	if len(docs) != 3 || !strings.Contains(string(docs[1]), "not a separator") || string(docs[2]) != "kind: B\n" {
		t.Errorf("%q", docs)
	}
}

func TestFileInLayer(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"./other": "x", "package.yaml": "kind: Provider\n"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	b, err := fileInLayer(buf.Bytes(), "package.yaml")
	if err != nil || string(b) != "kind: Provider\n" {
		t.Errorf("%q %v", b, err)
	}
	if _, err := fileInLayer(buf.Bytes(), "missing.yaml"); err == nil {
		t.Error("a missing file was found")
	}
}

func TestLockedVersion(t *testing.T) {
	p := t.TempDir() + "/.terraform.lock.hcl"
	lock := "provider \"registry.terraform.io/grafana/grafana\" {\n  version     = \"4.53.1\"\n  constraints = \"~> 4.53\"\n}\n"
	if err := os.WriteFile(p, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := lockedVersion(p); v != "4.53.1" {
		t.Errorf("version = %q", v)
	}
}
