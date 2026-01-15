FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'main.Version=${VERSION}-plus' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./ModelGatePlus ./cmd/server/

FROM alpine:3.22.0

RUN apk add --no-cache tzdata

RUN mkdir /ModelGate

COPY --from=builder ./app/ModelGatePlus /ModelGate/ModelGatePlus

COPY config.example.yaml /ModelGate/config.example.yaml

WORKDIR /ModelGate

EXPOSE 8317

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

CMD ["./ModelGatePlus"]