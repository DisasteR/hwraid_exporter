# Copyright 2026 Benjamin Saiz
# SPDX-License-Identifier: Apache-2.0
# Image de l'exporter : binaire statique seul (docs/deploiement.md).
# Image de compilation épinglée par empreinte ; version de Go = go.mod.
FROM docker.io/library/golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY LICENSE NOTICE ./
COPY scripts/collect-licenses.sh ./scripts/
# Sans .git dans le contexte, la version et la révision sont passées en
# arguments (tag et commit en publication, git describe en CI).
ARG VERSION=dev
ARG REVISION=
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" -o /out/hwraid-exporter ./cmd/hwraid-exporter
# Licences de tout ce qui est compilé dans le binaire : ce projet, la
# bibliothèque standard Go et chaque module (le script échoue sur un module
# sans licence).
RUN mkdir -p /out/licenses/hwraid_exporter /out/licenses/go && cp LICENSE NOTICE /out/licenses/hwraid_exporter/ \
 && cp "$(go env GOROOT)/LICENSE" /out/licenses/go/ \
 && sh scripts/collect-licenses.sh ./cmd/hwraid-exporter /out/licenses

# Aucun shell ni bibliothèque. Le processus démarre en root pour préparer
# ses nœuds dans le /dev du conteneur, puis abandonne lui-même ses
# privilèges (UID 65534, CAP_SYS_RAWIO seule) : pas de directive USER.
FROM scratch
ARG VERSION=dev
ARG REVISION=
LABEL org.opencontainers.image.source="https://github.com/DisasteR/hwraid_exporter" \
      org.opencontainers.image.description="Exporter Prometheus pour contrôleurs RAID matériels" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/hwraid-exporter /hwraid-exporter
COPY --from=build /out/licenses /licenses
EXPOSE 9799
ENTRYPOINT ["/hwraid-exporter"]
CMD ["serve", "-listen", "0.0.0.0:9799"]
