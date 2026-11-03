# syntax=docker/dockerfile:1
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/cistern ./cmd/cistern

FROM debian:bookworm-slim
ARG PG_MAJOR=16
# TODO: not verified yet. Add the PGDG repository to pin postgresql-client-${PG_MAJOR} and a MySQL client;
# until then the postgres and mysql drivers do not work in this image.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl redis-tools age \
    && rm -rf /var/lib/apt/lists/*
RUN useradd -u 10001 -r -s /usr/sbin/nologin cistern
COPY --from=build /out/cistern /usr/local/bin/cistern
USER 10001
ENTRYPOINT ["/usr/local/bin/cistern"]
