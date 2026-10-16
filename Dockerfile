# Release image: copies the prebuilt static binaries from dist/ (run make release first).
# The final stage has no RUN, so a multi-arch build needs no emulation.
FROM --platform=$BUILDPLATFORM docker.io/library/alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 AS base
RUN mkdir -p /out/data && chown 65532:65532 /out/data && chmod 0700 /out/data

FROM scratch
ARG TARGETARCH
ARG TARGETVARIANT
LABEL org.opencontainers.image.source="https://github.com/M-Wham/sockswebui" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="SOCKS5 proxy slots routed through WireGuard and upstream proxies, managed from a web UI"
COPY --from=base /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=base --chown=65532:65532 /out/data /data
COPY dist/sockswebui-linux-${TARGETARCH}${TARGETVARIANT} /sockswebui
USER 65532:65532
VOLUME /data
EXPOSE 8787
ENTRYPOINT ["/sockswebui","--data-dir","/data"]
