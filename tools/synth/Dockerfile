FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
# Зависимости отдельным слоем: он пересобирается только при изменении go.mod и go.sum.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/synth .

# Статический бинарник без ОС: нечего обновлять и нечем воспользоваться при взломе.
FROM scratch
COPY --from=build /out/synth /synth
USER 65532:65532
ENTRYPOINT ["/synth"]
