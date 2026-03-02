FROM golang:1.25 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /app/hypernet-node ./cmd/node

FROM alpine:3.20

RUN adduser -D -g '' appuser
USER appuser

WORKDIR /app

COPY --from=builder /app/hypernet-node /usr/local/bin/hypernet-node

EXPOSE 4001/tcp

ENTRYPOINT ["hypernet-node"]

