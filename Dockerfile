FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/note-agent-server ./cmd/note-agent-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/note-agent-server /note-agent-server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/note-agent-server"]
