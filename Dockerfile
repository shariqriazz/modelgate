FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./modelgate ./cmd/server/

FROM alpine:3.22.0

RUN apk add --no-cache tzdata ca-certificates

WORKDIR /app

COPY --from=builder /app/modelgate /app/modelgate

COPY config.example.yaml /app/config.example.yaml

EXPOSE 4091

CMD ["./modelgate", "-config", "config.yaml"]
