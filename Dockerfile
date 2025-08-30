FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o /linkwatch ./cmd/linkwatch

FROM scratch

COPY --from=builder /linkwatch /linkwatch

EXPOSE 8080

ENTRYPOINT ["/linkwatch"]
