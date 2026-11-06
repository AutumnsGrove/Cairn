# Sepia's image. Two build stages and one small run stage: the potato pulls the image and compiles nothing.
#
# S00 builds this locally to prove the Dockerfile works. CI publishes it by digest, multi-arch, in S13.

FROM --platform=$BUILDPLATFORM node:26-alpine AS web
WORKDIR /src/web
# Node 25 and later no longer ship corepack, so pnpm is installed directly at a pinned version.
RUN npm install -g pnpm@10.32.1
# The lockfile first, so a source-only change does not reinstall the world.
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm run build

FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS build
WORKDIR /src
COPY go.mod ./
COPY . .
# The frontend the web stage built replaces the placeholder the repository carries.
COPY --from=web /src/web/build ./internal/webui/dist
ARG TARGETOS=linux
ARG TARGETARCH=arm64
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/AutumnsGrove/sepia/internal/version.Version=${VERSION}" \
      -o /out/sepia ./cmd/sepia

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sepia /sepia
# The service is loopback only; `tailscale serve` on the host is the way in (docs/STANDARDS.md 6).
EXPOSE 8893
USER nonroot:nonroot
ENTRYPOINT ["/sepia", "serve"]
