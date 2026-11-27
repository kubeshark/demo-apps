package chartcontract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubeshark/demo-apps/internal/flows/wallet"
	"gopkg.in/yaml.v3"
)

func TestWalletCaptureFixture(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("wallet-capture validation requires Helm")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("wallet-capture validation requires Python 3")
	}
	dir := t.TempDir()
	helm(t, "package", "../../deploy/chart", "--version", "0.3.1-capture-test", "--app-version", "unused-branch-image", "--destination", dir)
	archive := filepath.Join(dir, "demo-apps-0.3.1-capture-test.tgz")
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	assets := map[string][]byte{}
	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			assets[strings.TrimPrefix(header.Name, "demo-apps/")] = body
		}
	}
	var metadata contract
	if err := yaml.Unmarshal(assets["files/contracts/workload.yaml"], &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Functional.Fixtures["wallet-capture"].Readiness != "traffic-success" {
		t.Fatal("wallet-capture must advertise successful startup HTTP traffic readiness")
	}
	preset := filepath.Join(dir, "functional.yaml")
	if err := os.WriteFile(preset, assets[metadata.Functional.PresetPath], 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"template", "demo-apps", archive, "--namespace", "capture-test", "-f", preset, "--set", "fixture.name=wallet-capture"}
	rendered := helm(t, args...)
	deployed := testWalletCaptureDocuments(t, rendered)
	for _, invalid := range []string{"fixture.interval=1s", "fixture.markerPrefix=unexpected", "fixture.markerCount=1", "global.imageTag=not-v0.3.0"} {
		command := append(append([]string{}, args...), "--set", invalid)
		if out, err := exec.Command("helm", command...).CombinedOutput(); err == nil {
			t.Fatalf("wallet-capture accepts invalid override %s: %s", invalid, out)
		}
	}
	for _, name := range []string{"wallet_capture.py", "wallet-markers.tsv"} {
		if deployed[name] == "" {
			t.Fatalf("missing deployed asset %s", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(deployed[name]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(assets["files/contracts/wallet-capture.schema.json"], &schema); err != nil || schema["$defs"] == nil {
		t.Fatalf("missing or invalid control ledger schema: %v", err)
	}
	cmd := exec.Command("python3", "test_wallet_capture.py", "-v")
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "CAPTURE_SCRIPT="+filepath.Join(dir, "wallet_capture.py"), "CAPTURE_MARKERS="+filepath.Join(dir, "wallet-markers.tsv"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("packaged controller tests: %v\n%s", err, out)
	}
	// Compare the deployed generator with the real gateway's bytes.
	code := `import importlib.util, hashlib, sys
spec = importlib.util.spec_from_file_location("capture", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
v = m.markers(sys.argv[2])
for suffix, size in (("UNDER", 1 << 20), ("OVER", 3 << 20)):
    print(hashlib.sha256(m.statement(v, suffix, size)).hexdigest())
`
	cmd = exec.Command("python3", "-c", code, filepath.Join(dir, "wallet_capture.py"), filepath.Join(dir, "wallet-markers.tsv"))
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	want := fmt.Sprintf("%x\n%x\n", sha256.Sum256(wallet.Statement("UNDER", wallet.StatementUnder)), sha256.Sum256(wallet.Statement("OVER", wallet.StatementOver)))
	if err != nil || string(out) != want {
		t.Fatalf("controller statement differs from real wallet.Statement: %v\n%s", err, out)
	}
}

type captureDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Replicas int            `yaml:"replicas"`
		Selector map[string]any `yaml:"selector"`
		Ports    []struct {
			Port int `yaml:"port"`
		} `yaml:"ports"`
		Template struct {
			Metadata struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec struct {
				Containers []struct {
					Name    string   `yaml:"name"`
					Image   string   `yaml:"image"`
					Command []string `yaml:"command"`
					Env     []struct {
						Name  string `yaml:"name"`
						Value string `yaml:"value"`
					} `yaml:"env"`
					ReadinessProbe struct {
						HTTPGet struct {
							Path string `yaml:"path"`
							Port string `yaml:"port"`
						} `yaml:"httpGet"`
					} `yaml:"readinessProbe"`
				} `yaml:"containers"`
				Affinity struct {
					PodAntiAffinity struct {
						Required []struct {
							TopologyKey   string `yaml:"topologyKey"`
							LabelSelector struct {
								MatchLabels map[string]string `yaml:"matchLabels"`
							} `yaml:"labelSelector"`
						} `yaml:"requiredDuringSchedulingIgnoredDuringExecution"`
					} `yaml:"podAntiAffinity"`
				} `yaml:"affinity"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func testWalletCaptureDocuments(t *testing.T, rendered []byte) map[string]string {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(rendered))
	deployments, services := map[string]bool{}, map[string]bool{}
	deployed := map[string]string{}
	for {
		var doc captureDocument
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if doc.Kind == "" {
			continue
		}
		if doc.Metadata.Labels["demo-apps.kubeshark.co/fixture"] != "wallet-capture" {
			t.Fatalf("unidentified fixture resource %s/%s", doc.Kind, doc.Metadata.Name)
		}
		if strings.Contains(doc.Metadata.Name, "runner") {
			t.Fatal("autonomous runner rendered")
		}
		switch doc.Kind {
		case "ConfigMap":
			for key, value := range doc.Data {
				if _, duplicate := deployed[key]; duplicate {
					t.Fatalf("duplicate deployed asset %s", key)
				}
				deployed[key] = value
			}
		case "Service":
			services[doc.Metadata.Name] = true
			port := 8080
			if doc.Metadata.Name == "http-capture-client" {
				port = 8081
			}
			if doc.Spec.Selector["app"] != doc.Metadata.Name || len(doc.Spec.Ports) != 1 || doc.Spec.Ports[0].Port != port {
				t.Fatalf("wrong service selector/port: %s", doc.Metadata.Name)
			}
		case "Deployment":
			name := doc.Metadata.Name
			deployments[name] = true
			if doc.Spec.Replicas != 1 || doc.Spec.Template.Metadata.Labels["app"] != name {
				t.Fatalf("wrong replica count or app selector: %s", name)
			}
			opposite := "http-capture-client"
			if name == opposite {
				opposite = "http-capture-wallet"
			}
			rules := doc.Spec.Template.Spec.Affinity.PodAntiAffinity.Required
			if len(rules) != 1 || rules[0].TopologyKey != "kubernetes.io/hostname" || rules[0].LabelSelector.MatchLabels["app"] != opposite {
				t.Fatalf("missing required cross-node placement on %s", name)
			}
			names := map[string]bool{}
			pinned := map[string]string{
				"wallet":     "wallet:v0.3.0@sha256:13d45c5913678687d3da7c6cf33e3f3ae8ab3dedf4fe81b9019ce4db2bbd098b",
				"gateway":    "gateway:v0.3.0@sha256:c13efbabb68bdfcf856b646e0661d344d2eae5fe7f822bca5b07f765dfc71d5d",
				"controller": "client-python:v0.3.0@sha256:d531b8826a530382d51884e6f45d0b91c90d18b53912c7a02e2ef69e360eb9d3",
			}
			for _, container := range doc.Spec.Template.Spec.Containers {
				names[container.Name] = true
				if container.Name != "redis" && (pinned[container.Name] == "" || !strings.HasSuffix(container.Image, "/"+pinned[container.Name])) {
					t.Fatalf("changed application image: %s", container.Image)
				}
				if container.Name == "redis" && !strings.Contains(container.Image, "@sha256:") {
					t.Fatal("redis image is not digest pinned")
				}
				if container.Name == "gateway" {
					found := false
					for _, env := range container.Env {
						if env.Name == "WALLET_URL" && env.Value == "http://http-capture-wallet:8080" {
							found = true
						}
					}
					if !found {
						t.Fatal("gateway does not use real wallet")
					}
				}
			}
			if len(names) != 2 || (name == "http-capture-client" && (!names["gateway"] || !names["controller"])) ||
				(name == "http-capture-wallet" && (!names["wallet"] || !names["redis"])) {
				t.Fatalf("unexpected fixture containers: %v", names)
			}
		}
	}
	if len(deployments) != 2 || !deployments["http-capture-client"] || !deployments["http-capture-wallet"] || len(services) != 2 || !services["http-capture-client"] || !services["http-capture-wallet"] {
		t.Fatalf("incomplete fixture topology: deployments=%v services=%v", deployments, services)
	}
	return deployed
}
