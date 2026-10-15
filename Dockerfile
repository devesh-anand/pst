# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pure-Go SQLite driver, so the binary is fully static.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pst ./cmd/pst \
    && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pst /pst
COPY --from=build --chown=nonroot:nonroot /out/data /data
# Mount persistent storage at /data. There is deliberately no VOLUME here:
# platforms such as Railway reject it and attach volumes from their own config.
# The listen port defaults to 8080, or $PORT when the platform sets it.
ENV PST_DB_PATH=/data/pst.db
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/pst"]
