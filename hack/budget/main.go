// budget renders the chart with its default values and fails when any
// container lacks requests or limits, or when the totals exceed the budget the
// default profile promises: requests <= 1.5 GiB, limits <= 4 GiB of memory.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	maxRequests = 1536 << 20
	maxLimits   = 4096 << 20
)

type doc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []container `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type container struct {
	Name      string `yaml:"name"`
	Resources struct {
		Requests map[string]string `yaml:"requests"`
		Limits   map[string]string `yaml:"limits"`
	} `yaml:"resources"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: budget <chart-dir>")
		os.Exit(2)
	}
	out, err := exec.Command("helm", "template", "demo-apps", os.Args[1]).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "helm template:", err)
		os.Exit(1)
	}
	var reqTotal, limTotal int64
	failed := false
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var d doc
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d.Kind != "Deployment" && d.Kind != "StatefulSet" && d.Kind != "DaemonSet" {
			continue
		}
		for _, c := range d.Spec.Template.Spec.Containers {
			req, ok1 := quantity(c.Resources.Requests["memory"])
			lim, ok2 := quantity(c.Resources.Limits["memory"])
			_, ok3 := quantity(c.Resources.Requests["cpu"])
			_, ok4 := quantity(c.Resources.Limits["cpu"])
			if !ok1 || !ok2 || !ok3 || !ok4 {
				fmt.Printf("FAIL %s/%s container %s: missing cpu or memory requests/limits\n", d.Kind, d.Metadata.Name, c.Name)
				failed = true
				continue
			}
			fmt.Printf("%-40s memory %6d MiB / %6d MiB\n", d.Metadata.Name+"/"+c.Name, req>>20, lim>>20)
			reqTotal += req
			limTotal += lim
		}
	}
	fmt.Printf("total requests %d MiB (budget %d), limits %d MiB (budget %d)\n",
		reqTotal>>20, maxRequests>>20, limTotal>>20, maxLimits>>20)
	if reqTotal > maxRequests || limTotal > maxLimits {
		fmt.Println("FAIL: over budget")
		failed = true
	}
	if failed {
		os.Exit(1)
	}
}

func quantity(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0, false
	}
	return q.Value(), true
}
