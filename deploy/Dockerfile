# HatRack Server as a scratch image: only the static binary.
#   docker build -f deploy/Dockerfile -t hatrack .
#   docker run -d --name hatrack -p 127.0.0.1:8421:8421 -v hatrack-data:/data hatrack
#   docker exec hatrack hat server device add laptop
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X hatrack/internal/cli.Version=${VERSION}" -o /out/hat ./cmd/hat \
 && mkdir -p /out/data

FROM scratch
COPY --from=build /out/hat /hat
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV HATRACK_DATA=/data HATRACK_LISTEN=0.0.0.0:8421
EXPOSE 8421
VOLUME /data
ENTRYPOINT ["/hat"]
CMD ["server"]
