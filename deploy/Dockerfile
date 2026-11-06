FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY collector ./collector
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /nginx-inspector ./cmd/nginx-inspector
FROM scratch
COPY --from=build /nginx-inspector /nginx-inspector
HEALTHCHECK --interval=30s --timeout=5s CMD ["/nginx-inspector", "health"]
USER 1000:1000
ENTRYPOINT ["/nginx-inspector"]
CMD ["serve", "--db", "/data/events.sqlite3", "--control-file", "/control/control"]
