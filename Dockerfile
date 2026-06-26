FROM golang:1.22 AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o /reasoning-proxy .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /reasoning-proxy /reasoning-proxy
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/reasoning-proxy"]
