FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY EGATE.md ./
COPY templates/ ./templates/
RUN CGO_ENABLED=0 go build -trimpath -o /egate .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /egate /usr/local/bin/egate
ENV EGATE_HOST=0.0.0.0
EXPOSE 8119
ENTRYPOINT ["egate"]
