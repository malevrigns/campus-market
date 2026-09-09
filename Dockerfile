FROM golang:1.26.8-alpine@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS api-build
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/campus ./cmd/server

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS api
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 campus && adduser -D -u 10001 -G campus campus && mkdir -p /data/uploads && chown -R campus:campus /data
COPY --from=api-build /out/campus /usr/local/bin/campus
USER 10001:10001
WORKDIR /data
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/campus"]

FROM node:22-alpine@sha256:c610fcdfb1d5b4740dd70c284ed3cb16bb857e0f7166196e36a5501df7a3aa32 AS web-build
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM caddy:2-alpine@sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648 AS web
COPY --from=web-build /app/dist /srv
COPY deploy/Caddyfile /etc/caddy/Caddyfile
