FROM golang:1.25.12-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG SERVICE=./cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/corerank-service ${SERVICE}

FROM alpine:3.22

WORKDIR /app

RUN addgroup -S corerank && adduser -S -G corerank corerank

COPY --from=builder /out/corerank-service /app/corerank-service

USER corerank

EXPOSE 8081 18081 18082 19080 19081 19082

ENTRYPOINT ["/app/corerank-service"]
