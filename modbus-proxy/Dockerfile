FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/modbus-proxy ./cmd/modbus-proxy

FROM debian:bookworm-slim
RUN useradd --system --uid 65532 --home-dir /nonexistent --shell /usr/sbin/nologin proxy
USER proxy
COPY --from=build /out/modbus-proxy /usr/local/bin/modbus-proxy
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/modbus-proxy"]
