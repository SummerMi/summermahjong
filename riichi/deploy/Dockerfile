# riichi-server 多阶段镜像：Go 1.25 编译 → alpine 非 root 运行。
# 构建（在仓库根目录）：
#   docker build -f riichi/deploy/Dockerfile -t riichi-server .
# 运行：
#   docker run --rm -p 8099:8080 -e RIICHI_JWT_SECRET=change-me riichi-server -addr :8080
FROM golang:1.25 AS build
WORKDIR /src
COPY riichi/go.mod riichi/go.sum ./
RUN go mod download
COPY riichi/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/riichi-server ./cmd/riichi-server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 riichi
COPY --from=build /out/riichi-server /usr/local/bin/riichi-server
USER riichi
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/riichi-server"]
CMD ["-addr", ":8080"]
