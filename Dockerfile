# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src/go

# Keep dependency resolution independent from source changes. Every Go service
# uses this Dockerfile, so the explicit BuildKit cache IDs also prevent each
# service build from downloading and compiling the same module graph again.
COPY go/go.mod go/go.sum ./
RUN --mount=type=cache,id=kionga-go-mod,target=/go/pkg/mod \
    go mod download

COPY go/ ./
ARG SERVICE=gateway
RUN --mount=type=cache,id=kionga-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=kionga-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" \
    -o /service ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /service /service
EXPOSE 8080
ENTRYPOINT ["/service"]
