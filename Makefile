SERVICES := gateway runner catalog inventory
CHART := deploy/chart
MARKERS := $(wildcard internal/flows/*/markers.tsv)

.PHONY: all build test lint proto markers chart-lint budget check-generated images

all: build test lint chart-lint budget

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run ./...

proto:
	buf lint
	buf generate

# The chart ships each flow's markers.tsv as a ConfigMap. The copies under
# $(CHART)/files/markers are generated from the embedded originals and
# checked for drift in CI, so there is still one source.
markers:
	mkdir -p $(CHART)/files/markers
	for f in $(MARKERS); do cp $$f $(CHART)/files/markers/$$(basename $$(dirname $$f)).tsv; done

check-generated: proto markers
	git diff --exit-code -- gen $(CHART)/files

chart-lint:
	helm lint $(CHART)
	helm template demo-apps $(CHART) > /dev/null

# Go images are built by ko: one image per cmd named <KO_DOCKER_REPO>/<cmd>,
# distroless base from .ko.yaml.
TAG ?= dev
images:
	ko build --base-import-paths --platform=linux/amd64 --tags $(TAG) $(foreach s,$(SERVICES),./cmd/$(s))
