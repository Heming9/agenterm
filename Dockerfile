FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/agenterm-server/ ./cmd/agenterm-server/
COPY internal/ ./internal/

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/agenterm-server ./cmd/agenterm-server
RUN mkdir -m 1777 /out/tmp

FROM scratch
LABEL org.opencontainers.image.title="Agenterm Server" \
      org.opencontainers.image.source="https://github.com/Heming9/agenterm" \
      org.opencontainers.image.description="Remote terminal assistance over MCP"
COPY --from=build /out/agenterm-server /agenterm-server
COPY --from=build --chmod=1777 /out/tmp /tmp
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/agenterm-server"]
CMD ["-listen", "0.0.0.0:8080"]
