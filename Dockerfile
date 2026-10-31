# syntax=docker/dockerfile:1
# SMMYP – Show Me My Prompts. Statisches Go-Binary (ohne CGO) in einem distroless-Image.

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/smmyp ./cmd/smmyp \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="SMMYP" \
      org.opencontainers.image.description="Show Me My Prompts – Prompt-Archiv für das Heimnetz" \
      org.opencontainers.image.source="https://github.com/Buffer379x/smmyp"
COPY --from=build /out/smmyp /smmyp
# /data gehört bereits dem nonroot-Benutzer: Named Volumes funktionieren ohne chown.
COPY --from=build --chown=65532:65532 /out/data /data
ENV SMMYP_DATA_DIR=/data \
    SMMYP_LISTEN=:8080
VOLUME ["/data"]
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/smmyp", "healthcheck"]
ENTRYPOINT ["/smmyp"]
CMD ["serve"]
