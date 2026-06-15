VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD   := $(shell git rev-parse --short HEAD 2>/dev/null || echo source)
LDFLAGS := -X main.version=$(VERSION) -X main.build=$(BUILD)

all: build

vet:
	go vet ./...

test: vet
	go test -cover ./...

build: test
	go build -ldflags "$(LDFLAGS)" -o gorel .

install:
	go install -ldflags "$(LDFLAGS)" .
