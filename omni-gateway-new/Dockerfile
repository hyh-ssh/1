# Multi-stage image for the Go API and Vue admin UI.
FROM node:20-alpine AS frontend-build
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.23-alpine AS backend-build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Embed the compiled Vue application into the Go binary's static asset tree.
COPY --from=frontend-build /src/frontend/dist/ ./internal/httpapi/static/
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server

# Runtime image for the API (the frontend is available from the frontend target).
FROM alpine:3.20 AS backend
RUN addgroup -S app && adduser -S -G app app && apk add --no-cache ca-certificates tzdata
COPY --from=backend-build /out/server /usr/local/bin/server
USER app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=30s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:8080/readyz || exit 1
ENTRYPOINT ["/usr/local/bin/server"]

# Optional runtime image for the Vue preview server.
FROM node:20-alpine AS frontend
WORKDIR /app
COPY --from=frontend-build /src/frontend/package*.json ./
RUN npm ci
COPY --from=frontend-build /src/frontend/dist ./dist
USER node
EXPOSE 4173
CMD ["npm", "run", "preview", "--", "--host", "0.0.0.0", "--port", "4173"]
