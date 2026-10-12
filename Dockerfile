# syntax=docker/dockerfile:1
# Orchestrator image.
#
# Distroless: no shell, no package manager, nothing for an attacker to use if
# they get code execution in this container. The orchestrator holds database
# credentials and a service-account token, so its attack surface should be as
# close to zero as we can make it.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies first so that source-only changes reuse the module cache layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off: we want a static binary so the scratch/distroless base works.
# -trimpath removes local filesystem paths from the binary.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/orchestrator ./cmd/orchestrator \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/podmanager ./cmd/podmanager

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/orchestrator /usr/local/bin/orchestrator
COPY --from=build /out/podmanager   /usr/local/bin/podmanager

# 65532 is the conventional nonroot uid, already present in distroless.
USER 65532:65532

EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/orchestrator"]