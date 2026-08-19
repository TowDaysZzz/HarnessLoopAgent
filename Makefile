.PHONY: build run chat test vet check docker-build

build:
	go build -o bin/note-agent-server ./cmd/note-agent-server
	go build -o bin/note-agent-cli ./cmd/note-agent-cli

run:
	go run ./cmd/note-agent-server

chat:
	go run ./cmd/note-agent-cli -- "$(PROMPT)"

test:
	go test ./...

vet:
	go vet ./...

check: test vet

docker-build:
	docker build -t harness-loop-agent:dev .
