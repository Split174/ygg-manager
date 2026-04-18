FROM golang:1.25 AS builder

WORKDIR /app

COPY . .

RUN if [ ! -f go.mod ]; then go mod init ygg-manager; fi && go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o ygg-manager main.go

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /app/ygg-manager /ygg-manager

CMD ["/ygg-manager"]
