package chartcontract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type capability struct {
	MarkerPrefix bool   `yaml:"markerPrefix"`
	MarkerCount  bool   `yaml:"markerCount"`
	Interval     bool   `yaml:"interval"`
	Readiness    string `yaml:"readiness"`
}

type contract struct {
	SchemaVersion int `yaml:"schemaVersion"`
	SnapshotSeed  struct {
		Path   string `yaml:"path"`
		SHA256 string `yaml:"sha256"`
		Format string `yaml:"format"`
	} `yaml:"snapshotSeed"`
	Functional struct {
		PresetPath   string `yaml:"presetPath"`
		GenericFlows struct {
			ValuesPath string `yaml:"valuesPath"`
			ValuesKey  string `yaml:"valuesKey"`
		} `yaml:"genericFlows"`
		HTTPWarmupFixture string                `yaml:"httpWarmupFixture"`
		Fixtures          map[string]capability `yaml:"fixtures"`
	} `yaml:"functional"`
	Corpus struct {
		Flows               []string `yaml:"flows"`
		FlowInterval        string   `yaml:"flowInterval"`
		ClientInterval      string   `yaml:"clientInterval"`
		ExpectationsPath    string   `yaml:"expectationsPath"`
		DissectorValuesPath string   `yaml:"dissectorValuesPath"`
	} `yaml:"corpus"`
}

type container struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []struct {
		Name  string `yaml:"name"`
		Value string `yaml:"value"`
	} `yaml:"env"`
	ReadinessProbe struct {
		Exec struct {
			Command []string `yaml:"command"`
		} `yaml:"exec"`
	} `yaml:"readinessProbe"`
}

type document struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []container `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func helm(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %v: %v\n%s", args, err, out)
	}
	return out
}

func decodeDocuments(t *testing.T, data []byte) []document {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []document
	for {
		var doc document
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return docs
		}
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, doc)
	}
}

func TestPackagedWorkloadContract(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm is required for chart contract tests")
	}
	chart, err := filepath.Abs("../../deploy/chart")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	helm(t, "package", chart, "--destination", dir)
	archives, err := filepath.Glob(filepath.Join(dir, "*.tgz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("chart archives = %v, error = %v", archives, err)
	}
	f, err := os.Open(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close chart archive: %v", err)
		}
	}()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := gz.Close(); err != nil {
			t.Errorf("close chart gzip reader: %v", err)
		}
	}()
	tr := tar.NewReader(gz)
	assets := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			assets[strings.TrimPrefix(hdr.Name, "demo-apps/")] = body
		}
	}
	var metadata contract
	if err := yaml.Unmarshal(assets["files/contracts/workload.yaml"], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.SchemaVersion != 1 {
		t.Fatalf("schema version = %d", metadata.SchemaVersion)
	}
	t.Run("snapshotSeed", func(t *testing.T) {
		testSnapshotSeed(t, metadata, assets[metadata.SnapshotSeed.Path])
	})
	preset := filepath.Join(dir, "functional.yaml")
	if err := os.WriteFile(preset, assets[metadata.Functional.PresetPath], 0600); err != nil {
		t.Fatal(err)
	}
	render := func(t *testing.T, extra ...string) []document {
		args := []string{"template", "demo-apps", archives[0], "--namespace", "contract-test", "-f", preset}
		return decodeDocuments(t, helm(t, append(args, extra...)...))
	}
	var values map[string]any
	if err := yaml.Unmarshal(assets[metadata.Functional.GenericFlows.ValuesPath], &values); err != nil {
		t.Fatal(err)
	}
	flows, ok := values[metadata.Functional.GenericFlows.ValuesKey].(map[string]any)
	if !ok || len(flows) == 0 {
		t.Fatal("generic flow catalog is not available from packaged values")
	}
	for name := range flows {
		t.Run("flow/"+name, func(t *testing.T) {
			docs := render(t, "--set", "flows."+name+".enabled=true")
			for _, doc := range docs {
				if doc.Metadata.Name == "demo-apps-runner" && doc.Kind == "ConfigMap" {
					var cfg struct {
						Flows map[string]any `yaml:"flows"`
					}
					if err := yaml.Unmarshal([]byte(doc.Data["runner.yaml"]), &cfg); err != nil {
						t.Fatal(err)
					}
					if len(cfg.Flows) != 1 || cfg.Flows[name] == nil {
						t.Fatalf("selected flow %s rendered runner config %v", name, cfg.Flows)
					}
					return
				}
			}
			t.Fatal("selected flow has no runner configuration")
		})
	}
	var expectations map[string]any
	if err := yaml.Unmarshal(assets[metadata.Corpus.ExpectationsPath], &expectations); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{fmt.Sprint(expectations["window"]), metadata.Corpus.FlowInterval, metadata.Corpus.ClientInterval} {
		if duration, err := time.ParseDuration(value); err != nil || duration <= 0 {
			t.Fatalf("invalid measured coverage duration %q", value)
		}
	}
	var dissectors struct {
		Tap struct {
			Enabled []string `yaml:"enabledDissectors"`
		} `yaml:"tap"`
	}
	if err := yaml.Unmarshal(assets[metadata.Corpus.DissectorValuesPath], &dissectors); err != nil {
		t.Fatal(err)
	}
	enabled := map[string]bool{}
	for _, name := range dissectors.Tap.Enabled {
		enabled[name] = true
	}
	for name, floor := range expectations {
		if name == "window" {
			continue
		}
		if count, ok := floor.(int); !ok || count <= 0 {
			t.Fatalf("invalid coverage floor %s: %v", name, floor)
		}
		if name == "tls" {
			name = "tlsx"
		}
		if !enabled[name] {
			t.Fatalf("required coverage %s has no enabled dissector", name)
		}
	}
	for _, name := range metadata.Corpus.Flows {
		if flows[name] == nil {
			t.Fatalf("corpus selects unknown flow %s", name)
		}
	}
	corpusArgs := []string{"template", "demo-apps", archives[0], "--set", "clients.interval=" + metadata.Corpus.ClientInterval}
	for _, name := range metadata.Corpus.Flows {
		corpusArgs = append(corpusArgs, "--set", "flows."+name+".interval="+metadata.Corpus.FlowInterval)
	}
	corpusRunner := false
	for _, doc := range decodeDocuments(t, helm(t, corpusArgs...)) {
		if doc.Metadata.Name != "demo-apps-runner" || doc.Kind != "ConfigMap" {
			continue
		}
		var cfg struct {
			Flows map[string]struct {
				Interval string `yaml:"interval"`
			} `yaml:"flows"`
		}
		if err := yaml.Unmarshal([]byte(doc.Data["runner.yaml"]), &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Flows) != len(metadata.Corpus.Flows) {
			t.Fatalf("corpus renders %d flows, contract requires %d", len(cfg.Flows), len(metadata.Corpus.Flows))
		}
		for _, name := range metadata.Corpus.Flows {
			if cfg.Flows[name].Interval != metadata.Corpus.FlowInterval {
				t.Fatalf("corpus flow %s interval does not match measured contract", name)
			}
		}
		corpusRunner = true
	}
	if !corpusRunner {
		t.Fatal("corpus has no runner")
	}
	if metadata.Functional.Fixtures[metadata.Functional.HTTPWarmupFixture].Readiness != "traffic-success" {
		t.Fatal("HTTP warmup lacks traffic readiness")
	}
	for name, caps := range metadata.Functional.Fixtures {
		t.Run("fixture/"+name, func(t *testing.T) {
			args := []string{"--set", "fixture.name=" + name}
			if caps.MarkerPrefix {
				args = append(args, "--set-string", "fixture.markerPrefix=contract_test")
			}
			if caps.MarkerCount {
				args = append(args, "--set", "fixture.markerCount=3")
			}
			if caps.Interval {
				args = append(args, "--set", "fixture.interval=2s")
			}
			docs := render(t, args...)
			deployments, probes := 0, 0
			for _, doc := range docs {
				if doc.Kind != "Deployment" {
					continue
				}
				deployments++
				for _, c := range doc.Spec.Template.Spec.Containers {
					if len(c.ReadinessProbe.Exec.Command) == 0 || c.ReadinessProbe.Exec.Command[0] != "test" {
						continue
					}
					probes++
					script := ""
					if len(c.Args) > 0 {
						script = c.Args[len(c.Args)-1]
					} else if len(c.Command) > 0 {
						script = c.Command[len(c.Command)-1]
					}
					if script == "/scripts/send.sh" {
						for _, configMap := range docs {
							if configMap.Data["send.sh"] != "" {
								script = configMap.Data["send.sh"]
							}
						}
					}
					t.Run(c.Name+doc.Metadata.Name, func(t *testing.T) {
						switch c.Name {
						case "mizutest-kafka-py":
							testKafkaWrapper(t, renderedScript(t, docs, "produce-ready.py"))
						case "mizutest-outbound-tls-openssl":
							testTLSWrapper(t, renderedScript(t, docs, "ready.py"))
						default:
							testTrafficScript(t, script, c)
						}
						if name == metadata.Functional.HTTPWarmupFixture {
							testHTTPReadiness(t, script, c)
						}
						if name == "http-markers" || name == "resilience-traffic" {
							testMarkerHTTPReadiness(t, script, c)
						}
					})
				}
			}
			if deployments == 0 || (caps.Readiness == "traffic-success" && probes == 0) {
				t.Fatalf("fixture deployments=%d traffic probes=%d", deployments, probes)
			}
		})
	}
	for _, args := range [][]string{
		{"--set", "fixture.name=unknown"},
		{"--set", "fixture.name=http", "--set", "fixture.markerCount=1"},
		{"--set", "fixture.name=http", "--set", "fixture.interval=1s"},
		{"--set", "fixture.name=openssl-mysql"},
	} {
		cmdArgs := append([]string{"template", "demo-apps", archives[0], "-f", preset}, args...)
		if out, err := exec.Command("helm", cmdArgs...).CombinedOutput(); err == nil {
			t.Fatalf("invalid selection accepted: %v\n%s", args, out)
		}
	}
}

func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("traffic script condition did not become true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startScript(t *testing.T, script string, env []string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(log.String())
		}
	})
}

func testTrafficScript(t *testing.T, script string, c container) {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	calls, sleeps := filepath.Join(dir, "calls"), filepath.Join(dir, "sleeps")
	allow, advance := filepath.Join(dir, "allow"), filepath.Join(dir, "advance")
	for _, name := range []string{"curl", "mysql", "psql", "mongosh", "main"} {
		body := "#!/bin/sh\ncase \"$*\" in *'SELECT 1'*|*'CREATE TABLE'*) exit 0;; esac\nprintf '%s %s\\n' \"$0\" \"$*\" >> \"$CALLS\"\ntest -f \"$ALLOW\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	sleep := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$SLEEPS\"\nwhile ! test -f \"$ADVANCE\"; do /bin/sleep 0.01; done\nrm -f \"$ADVANCE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte(sleep), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(ready)
	env := []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "CALLS=" + calls, "SLEEPS=" + sleeps, "ALLOW=" + allow, "ADVANCE=" + advance}
	for _, item := range c.Env {
		env = append(env, item.Name+"="+item.Value)
	}
	script = strings.ReplaceAll(script, "./main", filepath.Join(dir, "main"))
	startScript(t, strings.ReplaceAll(script, "/tmp/demo-apps-ready", ready), env)
	readLines := func(path string) []string {
		data, _ := os.ReadFile(path)
		if len(data) == 0 {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	eventually(t, func() bool { return len(readLines(sleeps)) == 1 })
	probe := append([]string(nil), c.ReadinessProbe.Exec.Command...)
	for i := range probe {
		probe[i] = strings.ReplaceAll(probe[i], "/tmp/demo-apps-ready", ready)
	}
	if err := exec.Command(probe[0], probe[1:]...).Run(); err == nil {
		t.Fatal("failed operation or stale marker made the client ready")
	}
	first := readLines(calls)
	if len(first) == 0 {
		t.Fatal("client did not attempt traffic before readiness")
	}
	write(allow)
	write(advance)
	eventually(t, func() bool { return len(readLines(sleeps)) == 2 })
	for range 3 {
		if err := exec.Command(probe[0], probe[1:]...).Run(); err != nil {
			t.Fatalf("successful traffic did not make client ready: %v", err)
		}
	}
	second := readLines(calls)
	if len(second) != 2*len(first) {
		t.Fatalf("retry or readiness changed traffic count: first=%d total=%d", len(first), len(second))
	}
	if got := readLines(sleeps); got[0] != got[1] {
		t.Fatalf("retry changed cadence: %v", got)
	}
	for _, item := range c.Env {
		if item.Name == "EXPECTED_MARKERS" {
			for i := 1; i <= 3; i++ {
				if !strings.Contains(strings.Join(first, "\n"), fmt.Sprintf("contract_test_seq%d", i)) {
					t.Fatalf("batch lost marker %d: %v", i, first)
				}
			}
		}
	}
}

func testHTTPReadiness(t *testing.T, script string, c container) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required for HTTP readiness behavior")
	}
	var success atomic.Bool
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if !success.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(server.Close)
	ready := filepath.Join(t.TempDir(), "ready")
	script = strings.ReplaceAll(script, "/tmp/demo-apps-ready", ready)
	script = strings.ReplaceAll(script, "http://http-server", server.URL)
	startScript(t, script, nil)
	eventually(t, func() bool { return requests.Load() > 0 })
	if _, err := os.Stat(ready); !os.IsNotExist(err) {
		t.Fatalf("HTTP 503 produced readiness: %v", err)
	}
	success.Store(true)
	eventually(t, func() bool { _, err := os.Stat(ready); return err == nil })
	probe := c.ReadinessProbe.Exec.Command
	args := make([]string, len(probe)-1)
	for i, arg := range probe[1:] {
		args[i] = strings.ReplaceAll(arg, "/tmp/demo-apps-ready", ready)
	}
	if err := exec.Command(probe[0], args...).Run(); err != nil {
		t.Fatal(err)
	}
}

func testMarkerHTTPReadiness(t *testing.T, script string, c container) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required for marker readiness behavior")
	}
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	ready := filepath.Join(t.TempDir(), "ready")
	script = strings.NewReplacer(
		"/tmp/demo-apps-ready", ready,
		"http://http-markers-server", server.URL,
		"http://resilience-server", server.URL,
	).Replace(script)
	var env []string
	for _, item := range c.Env {
		env = append(env, item.Name+"="+item.Value)
	}
	startScript(t, script, env)
	eventually(t, func() bool { _, err := os.Stat(ready); return err == nil })
	probe := c.ReadinessProbe.Exec.Command
	args := make([]string, len(probe)-1)
	for i, arg := range probe[1:] {
		args[i] = strings.ReplaceAll(arg, "/tmp/demo-apps-ready", ready)
	}
	if err := exec.Command(probe[0], args...).Run(); err != nil {
		t.Fatalf("completed marker request did not make client ready: %v", err)
	}
}

func testKafkaWrapper(t *testing.T, wrapper []byte) {
	t.Helper()
	for _, mode := range []string{"success", "delivery-error", "pending", "missing-delivery"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			producer := filepath.Join(dir, "produce.py")
			fixture := "from confluent_kafka import Producer\np = Producer({'bootstrap.servers': 'kafka:9092'})\nfor data in ['a', 'b', 'c']:\n    p.poll(0)\n    p.produce('mytopic', data.encode('utf-8'), callback=lambda err, msg: None)\np.flush()\n"
			if err := os.WriteFile(producer, []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			source := strings.ReplaceAll(string(wrapper), "/app/produce.py", producer)
			source = strings.ReplaceAll(source, "/tmp/demo-apps-ready", ready)
			path := filepath.Join(dir, "wrapper.py")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			driver := `
import pathlib, runpy, sys, types
mode, wrapper, ready = sys.argv[1:]
calls, callbacks = [], []
class Producer:
    def __init__(self, config):
        assert config == {'bootstrap.servers': 'kafka:9092'}
    def poll(self, timeout):
        assert timeout == 0
    def produce(self, topic, data, callback):
        calls.append((topic, data))
        callbacks.append(callback)
    def flush(self):
        for index, callback in enumerate(callbacks):
            if mode == 'missing-delivery' and index == 2:
                continue
            callback('failed' if mode == 'delivery-error' and index == 1 else None, object())
        return 1 if mode == 'pending' else 0
module = types.ModuleType('confluent_kafka')
module.Producer = Producer
sys.modules['confluent_kafka'] = module
runpy.run_path(wrapper)
assert calls == [('mytopic', b'a'), ('mytopic', b'b'), ('mytopic', b'c')], calls
assert pathlib.Path(ready).exists() == (mode == 'success')
`
			if out, err := exec.Command("python3", "-c", driver, mode, path, ready).CombinedOutput(); err != nil {
				t.Fatalf("Kafka wrapper %s: %v\n%s", mode, err, out)
			}
		})
	}
}

func testTLSWrapper(t *testing.T, wrapper []byte) {
	t.Helper()
	for _, mode := range []string{"success", "http-error", "request-error"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			client := filepath.Join(dir, "example.py")
			fixture := "import requests\nfor method, url in [('POST', 'https://example.test/users'), ('GET', 'https://example.test/users'), ('GET', 'https://example.test/posts')]:\n    try:\n        requests.request(method, url, headers={'x-test': 'safe'})\n    except RuntimeError:\n        pass\n"
			if err := os.WriteFile(client, []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			source := strings.ReplaceAll(string(wrapper), "/app/example.py", client)
			source = strings.ReplaceAll(source, "/tmp/demo-apps-ready", ready)
			path := filepath.Join(dir, "wrapper.py")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ready, nil, 0600); err != nil {
				t.Fatal(err)
			}
			driver := `
import pathlib, runpy, sys, types
mode, wrapper, ready = sys.argv[1:]
calls = []
def request(method, url, **kwargs):
    assert kwargs == {'headers': {'x-test': 'safe'}}, kwargs
    assert pathlib.Path(ready).exists() == (mode == 'success' and len(calls) > 0)
    calls.append((method, url))
    if mode == 'request-error':
        raise RuntimeError('connection failed')
    return types.SimpleNamespace(status_code=201 if mode == 'success' else 503)
module = types.ModuleType('requests')
module.request = request
sys.modules['requests'] = module
runpy.run_path(wrapper)
assert calls == [('POST', 'https://example.test/users'), ('GET', 'https://example.test/users'), ('GET', 'https://example.test/posts')], calls
assert pathlib.Path(ready).exists() == (mode == 'success')
`
			if out, err := exec.Command("python3", "-c", driver, mode, path, ready).CombinedOutput(); err != nil {
				t.Fatalf("TLS wrapper %s: %v\n%s", mode, err, out)
			}
		})
	}
}

func renderedScript(t *testing.T, docs []document, key string) []byte {
	t.Helper()
	for _, doc := range docs {
		if script := doc.Data[key]; script != "" {
			return []byte(script)
		}
	}
	t.Fatalf("rendered chart has no script %s", key)
	return nil
}
