.PHONY: build run test vet check docker-build

build:
	go build -o bin/note-agent-server ./cmd/note-agent-server

run:
	go run ./cmd/note-agent-server

test:
	go test ./...

vet:
	go vet ./...

check: test vet

docker-build:
	docker build -t harness-loop-agent:dev .
