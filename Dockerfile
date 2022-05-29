FROM ghcr.io/seankhliao/gotip AS build
WORKDIR /workspace
ENV CGO_ENABLED=0 \
    GOFLAGS=-trimpath
COPY go.* ./
RUN go mod download
COPY . .
RUN go test -vet=all ./... && \
    go build ./...
