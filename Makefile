.PHONY: build run chat test test-race integration-rag integration-mysql vet check docker-build

build:
	go build -o bin/note-agent-server ./cmd/note-agent-server
	go build -o bin/note-agent-cli ./cmd/note-agent-cli

run:
	go run ./cmd/note-agent-server

chat:
	go run ./cmd/note-agent-cli -- "$(PROMPT)"

test:
	go test ./...

test-race:
	go test -race ./internal/...

integration-rag:
	test "$(RAG_INTEGRATION)" = "1"
	test -n "$(RAG_BASE_URL)"
	test -n "$(RAG_API_KEY)"
	test -n "$(RAG_INTEGRATION_KB_ID)"
	go test -v ./internal/ragclient -run TestIntegrationRetrieve

integration-mysql:
	test -n "$(MYSQL_INTEGRATION_DSN)"
	go test -v ./internal/platform/mysqlstore -run TestIntegrationRepositoryLifecycle

vet:
	go vet ./...

check: test vet

docker-build:
	docker build -t harness-loop-agent:dev .
