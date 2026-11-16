FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
# Зависимости отдельным слоем: он пересобирается только при изменении go.mod и go.sum.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/overlay-api .

# Статический бинарник без ОС. Проверку здоровья делает он сам: флаг -healthcheck.
FROM scratch
COPY --from=build /out/overlay-api /overlay-api
COPY config.yaml /etc/overlay-api/config.yaml
ENV OVERLAY_CONFIG=/etc/overlay-api/config.yaml
USER 65532:65532
EXPOSE 8787
ENTRYPOINT ["/overlay-api"]
