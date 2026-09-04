# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/headhunter-core ./cmd/headhunter-core

# distroless/static:nonroot -> ~0 CVE, uid 65532
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/headhunter-core /headhunter-core
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/headhunter-core"]
