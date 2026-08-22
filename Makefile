GOARCH ?= amd64
DIST_DIR ?= dist/linux_$(GOARCH)

.PHONY: noop
noop:

.PHONY: run
run:
	go run ./cli/pprotein

.PHONY: run-agent
run-agent:
	go run ./cli/pprotein-agent

.PHONY: build
build: cbdprotein pprotein-agent

cbdprotein: view/dist
	go build -trimpath -ldflags="-w -s" -o cbdprotein ./cli/pprotein

pprotein-agent:
	go build -trimpath -ldflags="-w -s" ./cli/pprotein-agent

# Cross-compile for the ISUCON servers. Copy $(DIST_DIR) into isucon-kit-v2/bin/.
.PHONY: build-linux
build-linux: view/dist
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags="-w -s" -o $(DIST_DIR)/cbdprotein ./cli/pprotein
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags="-w -s" -o $(DIST_DIR)/pprotein-agent ./cli/pprotein-agent

view/dist:
	npm --prefix view ci
	npm --prefix view run build

.PHONY: clean
clean:
	rm -rf cbdprotein pprotein-agent dist view/dist
