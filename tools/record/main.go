// Command record saves a real instance's answers into testdata/api, where the
// unit tests replay them. It takes the connection settings from the
// environment variables the description names (GRAFANA_URL, ...), runs check
// and discover with the provider's record mode on, and replaces testdata/api
// only when both read everything.
//
//	GRAFANA_URL=http://localhost:3300 GRAFANA_AUTH=admin:admin go run ./tools/record
//
// Record only a throwaway instance filled with test objects: answers can hold
// secrets.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/hayone1/gitopsify-provider-grafana/internal/grafana"
	"github.com/hayone1/gitopsify/sdk"
)

func main() {
	out := flag.String("out", "testdata/api", "directory to replace with the recorded answers")
	partial := flag.Bool("partial", false, "keep the recording even if something couldn't be read")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, grafana.New("dev"), *out, *partial); err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, p sdk.Provider, out string, partial bool) error {
	d := p.Describe()
	settings, err := fromEnv(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(out)), 0o755); err != nil {
		return err
	}
	// Next to out, so the final rename stays on one file system.
	tmp, err := os.MkdirTemp(filepath.Dir(filepath.Clean(out)), ".record-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Setenv(grafana.RecordEnv, tmp); err != nil {
		return err
	}
	req := sdk.Request{APIVersion: sdk.APIVersion, Instance: "record", Settings: settings}

	var stderr bytes.Buffer
	answer, err := call(ctx, p, sdk.MethodCheck, req, &stderr)
	if err != nil {
		return failed("check", err, &stderr)
	}
	check, err := sdk.ReadCheck(bytes.NewReader(answer))
	if err != nil {
		return failed("check", err, &stderr)
	}
	fmt.Printf("connected to %s as %s, %d %s\n", check.Target, check.Identity, len(check.Partitions), d.Partition.Plural)
	var problems []string
	problems = append(problems, check.Warnings...)
	for _, k := range check.Kinds {
		if !k.OK {
			problems = append(problems, fmt.Sprintf("kind %s: %s", k.Kind, k.Message))
		}
	}

	answer, err = call(ctx, p, sdk.MethodDiscover, req, &stderr)
	if err != nil {
		return failed("discover", err, &stderr)
	}
	inv, err := sdk.ReadInventory(bytes.NewReader(answer), nil)
	if err != nil {
		return failed("discover", err, &stderr)
	}
	fmt.Printf("read %d objects (%d skipped) in %d %s\n", len(inv.Resources), len(inv.Skipped), len(inv.Partitions), d.Partition.Plural)
	for _, w := range inv.Warnings {
		problems = append(problems, fmt.Sprintf("%s %s: %s", w.Partition, w.Kind, w.Message))
	}
	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "not everything could be read, so the tests would miss answers:")
		for _, pr := range problems {
			fmt.Fprintln(os.Stderr, "  -", pr)
		}
		if !partial {
			return errors.New("testdata unchanged; fix the access (or pass -partial)")
		}
	}

	files, _ := filepath.Glob(filepath.Join(tmp, "*"))
	if len(files) == 0 {
		return fmt.Errorf("nothing was recorded: does the client save answers when %s is set?", grafana.RecordEnv)
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		return err
	}
	fmt.Printf("recorded %d answers in %s\n", len(files), out)
	return nil
}

// fromEnv reads every setting that has an environment variable.
func fromEnv(d sdk.Description) (map[string]string, error) {
	settings := map[string]string{}
	var missing []string
	for _, s := range d.Settings {
		v := ""
		if s.Env != "" {
			v = os.Getenv(s.Env)
		}
		if v == "" {
			v = s.Default
		}
		if v == "" {
			if s.Required {
				missing = append(missing, firstNonEmpty(s.Env, s.Name))
			}
			continue
		}
		settings[s.Name] = v
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("set %s (the connection to a throwaway instance)", strings.Join(missing, " and "))
	}
	return settings, nil
}

// call runs one method in-process, the way gitopsify runs the executable.
func call(ctx context.Context, p sdk.Provider, method string, req sdk.Request, stderr *bytes.Buffer) ([]byte, error) {
	in, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	sdk.Run(ctx, p, []string{method}, bytes.NewReader(in), &out, stderr)
	return out.Bytes(), nil
}

// failed adds the provider's log to an error.
func failed(method string, err error, stderr *bytes.Buffer) error {
	if stderr.Len() > 0 {
		fmt.Fprintf(os.Stderr, "%s log:\n%s", method, stderr.String())
	}
	return fmt.Errorf("%s: %w (testdata unchanged)", method, err)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
