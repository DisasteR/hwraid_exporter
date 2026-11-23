# Image de l'exporter : binaire statique seul (docs/deploiement.md).
# Image de compilation épinglée par empreinte ; version de Go = go.mod.
FROM docker.io/library/golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hwraid-exporter ./cmd/hwraid-exporter

# Aucun shell ni bibliothèque. Le processus démarre en root pour préparer
# ses nœuds dans le /dev du conteneur, puis abandonne lui-même ses
# privilèges (UID 65534, CAP_SYS_RAWIO seule) : pas de directive USER.
FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/DisasteR/hwraid_exporter" \
      org.opencontainers.image.description="Exporter Prometheus pour contrôleurs RAID matériels" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/hwraid-exporter /hwraid-exporter
EXPOSE 9799
ENTRYPOINT ["/hwraid-exporter"]
CMD ["serve", "-listen", "0.0.0.0:9799"]
