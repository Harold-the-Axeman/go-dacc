FROM alpine:3.7

RUN \
  apk add --update go git make gcc musl-dev linux-headers ca-certificates && \
  git clone --depth 1 https://github.com/daccproject/go-dacc && \
  (cd go-dacc && make gdacc) && \
  cp go-dacc/build/bin/gdacc /gdacc && \
  apk del go git make gcc musl-dev linux-headers && \
  rm -rf /go-dacc && rm -rf /var/cache/apk/*

EXPOSE 8545
EXPOSE 30303

ENTRYPOINT ["/gdacc"]
