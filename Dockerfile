FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o stock-data-extract ./cmd/

FROM alpine:3.20
ARG TARGETARCH
RUN apk add --no-cache ca-certificates tzdata wget tar chromium

# chromedp finds chromium-browser via PATH lookup — no extra env var needed
# Install Litestream for the target architecture
RUN wget -q "https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-linux-${TARGETARCH}.tar.gz" \
    && tar -xzf litestream-*.tar.gz -C /usr/local/bin/ \
    && rm litestream-*.tar.gz

RUN addgroup -S app && adduser -S -G app -u 1001 app

WORKDIR /app
COPY --from=builder /app/stock-data-extract .
COPY config.yaml .
COPY litestream.yml .
COPY start.sh .
RUN chmod +x start.sh \
    && mkdir -p /data \
    && chown -R app:app /app /data

USER app

CMD ["./start.sh"]
