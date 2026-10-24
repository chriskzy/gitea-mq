# Plain Docker build for hosts without Nix. The published image is built
# with Nix (nix build .#gitea-mq-docker); this produces an equivalent one.
#
#   docker build -t gitea-mq .
#   docker build --build-arg REVISION=$(git rev-parse HEAD) -t gitea-mq .

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gitea-mq ./cmd/gitea-mq

FROM alpine:3.22
ARG REVISION=""
# Merge branches are built by shelling out to git; HTTPS needs CA certs.
RUN apk add --no-cache git ca-certificates \
    && adduser -D -H -u 1000 gitea-mq \
    && mkdir -p /var/cache/gitea-mq \
    && chown gitea-mq /var/cache/gitea-mq
COPY --from=build /out/gitea-mq /usr/local/bin/gitea-mq
LABEL org.opencontainers.image.title="gitea-mq" \
      org.opencontainers.image.description="Merge queue for Gitea and GitHub" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://github.com/Mic92/gitea-mq" \
      org.opencontainers.image.revision="${REVISION}"
ENV XDG_CACHE_HOME=/var/cache
USER gitea-mq
VOLUME /var/cache/gitea-mq
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/gitea-mq"]
