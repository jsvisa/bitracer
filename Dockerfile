FROM node:22-alpine AS web
WORKDIR /app/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bitracer ./cmd/bitracer

FROM alpine:3.20
RUN adduser -D bitracer
COPY --from=build /bitracer /usr/local/bin/bitracer
COPY --from=web /app/web/dist /web/dist
ENV BITRACER_WEB_DIR=/web/dist
USER bitracer
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bitracer"]
CMD ["run"]
