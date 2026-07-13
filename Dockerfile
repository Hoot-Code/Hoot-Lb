FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /hoot-lb ./cmd/lb/

FROM scratch
COPY --from=builder /hoot-lb /hoot-lb
ENTRYPOINT ["/hoot-lb"]
