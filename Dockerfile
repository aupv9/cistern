# syntax=docker/dockerfile:1
FROM node:22.23.4-alpine@sha256:fe4f6d74e20370acb04ad889077d14d0f64a9c8ea6a42bc60cddf1e844699914 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
COPY web/scripts ./scripts
RUN npm ci --no-audit --no-fund
COPY api /src/api
COPY web ./
RUN npm run build && test -f dist/client/_shell.html

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY web/ui.go web/ui_stub.go ./web/
COPY --from=web /src/web/dist/client ./web/dist/client
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -tags timetzdata,ui -ldflags "-s -w -X main.version=${VERSION}" -o /out/cistern ./cmd/cistern

FROM oraclelinux:9-slim
ARG PG_MAJOR=16
RUN set -eux; \
    arch="$(uname -m)"; \
    rpm --import https://repo.mysql.com/RPM-GPG-KEY-mysql-2023; \
    rpm -q gpg-pubkey --qf '%{VERSION}\n' | grep -qx a8d3785c; \
    printf '[mysql-8.4-community]\nname=MySQL 8.4 Community\nbaseurl=https://repo.mysql.com/yum/mysql-8.4-community/el/9/$basearch/\nenabled=1\ngpgcheck=1\ngpgkey=https://repo.mysql.com/RPM-GPG-KEY-mysql-2023\nmodule_hotfixes=true\n' > /etc/yum.repos.d/mysql-community.repo; \
    rpm -i "https://download.postgresql.org/pub/repos/yum/reporpms/EL-9-${arch}/pgdg-redhat-repo-latest.noarch.rpm"; \
    microdnf install -y --nodocs --setopt=install_weak_deps=0 "postgresql${PG_MAJOR}" mysql-community-client; \
    microdnf clean all; \
    rm -rf /var/cache/yum /var/cache/dnf; \
    "/usr/pgsql-${PG_MAJOR}/bin/pg_dump" --version; \
    mysqldump --version
ENV PATH=/usr/pgsql-${PG_MAJOR}/bin:$PATH \
    CISTERN_CONFIG=/etc/cistern/cistern.yaml \
    CISTERN_TMPDIR=/tmp \
    HOME=/tmp
COPY --from=build /out/cistern /usr/local/bin/cistern
USER 10001:10001
EXPOSE 9464 9465 8443
ENTRYPOINT ["/usr/local/bin/cistern"]
CMD ["daemon"]
