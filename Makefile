GOARCH ?= amd64
DIST_DIR ?= dist/linux_$(GOARCH)

.PHONY: noop
noop:

.PHONY: run
run:
	go run ./cli/cbdprotein

.PHONY: run-agent
run-agent:
	go run ./cli/cbdprotein-agent

.PHONY: build
build: cbdprotein cbdprotein-agent

cbdprotein: view/dist
	go build -trimpath -ldflags="-w -s" -o cbdprotein ./cli/cbdprotein

cbdprotein-agent:
	go build -trimpath -ldflags="-w -s" -o cbdprotein-agent ./cli/cbdprotein-agent

# Cross-compile for the ISUCON servers. Copy $(DIST_DIR) into isucon-kit-v2/bin/.
.PHONY: build-linux
build-linux: view/dist
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags="-w -s" -o $(DIST_DIR)/cbdprotein ./cli/cbdprotein
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags="-w -s" -o $(DIST_DIR)/cbdprotein-agent ./cli/cbdprotein-agent

view/dist:
	npm --prefix view ci
	npm --prefix view run build

.PHONY: clean
clean:
	rm -rf cbdprotein cbdprotein-agent dist view/dist
