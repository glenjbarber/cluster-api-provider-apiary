# Build stage: compiles a static, CGO-free binary - this repo has no
# FreeBSD/PAM-style native dependency the apiary repo's own cmd/frontend
# has, so a plain cross-stage build works from any host arch.
FROM golang:1.27 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /manager ./cmd

# Final stage: distroless static base - no shell, no package manager,
# nothing beyond the binary and CA certs (needed for the real TLS
# connections this manager makes to restshimd - see internal/apiaryclient).
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
