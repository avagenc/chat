# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

ENV GOPRIVATE=go.avagenc.com/*

WORKDIR /app

COPY go.mod go.sum ./

RUN --mount=type=secret,id=gh_token \
    git config --global url."https://x-access-token:$(cat /run/secrets/gh_token)@github.com/".insteadOf "https://github.com/" \
    && go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -o server ./cmd/http/server.go

FROM gcr.io/distroless/static

WORKDIR /

COPY --from=builder /app/server .

EXPOSE 8080

CMD ["/server"]
