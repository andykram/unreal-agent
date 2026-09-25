.PHONY: build test check

build:
	go build -trimpath -o bin/unreal-agent-runner ./cmd/unreal-agent-runner
	go build -trimpath -o bin/unreal-agent-repl ./cmd/cli

test:
	go test -race ./...

check:
	go vet ./...
	@test -z "$$(gofmt -l cmd harness internal)" || { gofmt -l cmd harness internal; exit 1; }

.PHONY: workflow-prototype
workflow-prototype:
	./cmd/workflow-prototype/run.sh

.PHONY: workflow-prototype-branches
workflow-prototype-branches:
	./cmd/workflow-prototype/run.sh -script branching.py
