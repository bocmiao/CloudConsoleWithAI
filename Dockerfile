# Miao Panel（喵面板）Web 版
#
#   docker compose up -d            （见 docker-compose.yml）
#   docker logs miaopanel           （第一次启动时，这里有创建管理员账号用的初始化码）
#
# 在国内构建可以换 Go 模块代理：docker compose build --build-arg GOPROXY=https://goproxy.cn,direct

FROM golang:1.26-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=docker
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY=$GOPROXY go mod download
COPY . .
RUN CGO_ENABLED=0 GOPROXY=$GOPROXY go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/miaopanel ./cmd/miaopanel

FROM alpine:3
# ca-certificates: HTTPS to Tencent Cloud and the AI services.
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 miaopanel \
 && mkdir /data && chown miaopanel /data && chmod 700 /data
COPY --from=build /out/miaopanel /usr/local/bin/miaopanel
USER miaopanel
ENV MIAO_DATA=/data MIAO_LISTEN=0.0.0.0:18765 TZ=Asia/Shanghai
VOLUME /data
EXPOSE 18765
HEALTHCHECK --interval=1m --timeout=5s CMD wget -q -O /dev/null --header 'X-Miao: 1' http://127.0.0.1:18765/api/auth/state || exit 1
ENTRYPOINT ["miaopanel"]
CMD ["serve"]
