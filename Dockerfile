FROM golang:alpine AS builder
COPY . /root
WORKDIR /root
RUN go build

FROM alpine

LABEL org.opencontainers.image.source https://github.com/lennyerik/crawl4ai-proxy
LABEL org.opencontainers.image.description "A simple proxy that enables OpenWebUI to talk to crawl4ai"

COPY --from=builder /root/crawl-proxy /root
CMD ["/root/crawl-proxy"]
