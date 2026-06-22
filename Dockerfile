# Builds the headless CLI server only — the Wails desktop build needs a
# native GUI toolkit (WebKit/GTK) and doesn't make sense in a container.

FROM node:20-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# go:embed frontend/dist/* (main_cli.go) needs this present before `go build`.
COPY --from=frontend /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/memodump .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/memodump /memodump
ENV MEMODUMP_DATA=/data
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/memodump"]
