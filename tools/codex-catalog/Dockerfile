FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod *.go ./
RUN --network=none GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w" -o /out/codex-catalog .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/codex-catalog /codex-catalog
USER 65532:65532
ENV LISTEN_ADDR=:8080
ENTRYPOINT ["/codex-catalog"]
