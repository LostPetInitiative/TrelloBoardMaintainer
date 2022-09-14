# syntax=docker/dockerfile:1

## Build
FROM golang:1.19-alpine AS build

WORKDIR /app

COPY go.mod ./
COPY go.sum ./

RUN go mod download

# this image is to be run as a job or cronjob

COPY *.go ./
RUN go build -o /trelloBoardMaintainer


## Deploy
FROM alpine
WORKDIR /
COPY --from=build /trelloBoardMaintainer /trelloBoardMaintainer

# ENV TRELLO_KEY=xxxx
# ENV TRELLO_TOKEN=xxx
# ENV TRELLO_LIST=xxx
# ENV CARD_INACTIVITY_ARCHIVAL_THRESHOLD_HOURS=xxx

ENTRYPOINT ["/trelloBoardMaintainer"]