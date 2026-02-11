FROM --platform=$BUILDPLATFORM golang:1.23 AS builder
WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download
COPY ./ ./
ARG TARGETARCH
ARG TARGETOS
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -a -o jetkvm-api cmd/api/main.go

FROM scratch
COPY --from=builder /workspace/jetkvm-api /
ENTRYPOINT ["/jetkvm-api"]
