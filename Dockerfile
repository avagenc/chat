FROM golang:1.25-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -o server ./cmd/http/server.go

FROM gcr.io/distroless/static

WORKDIR /

COPY --from=builder /app/server .

EXPOSE 8080

CMD ["/server"]
