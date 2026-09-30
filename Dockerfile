# Builds Notes: a Go program on a small, non-root base image.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/notes . \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/notes /notes
# /data belongs to the app's user, so notes can be saved with or without a
# volume mounted there.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/notes"]
