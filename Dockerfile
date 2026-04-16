FROM --platform=$BUILDPLATFORM alpine:3

RUN apk add -U ca-certificates tzdata mailcap && rm -Rf /var/cache/apk/*

ARG TARGETARCH
COPY dist/selenwright_linux_$TARGETARCH /usr/bin/selenwright

EXPOSE 4444
ENTRYPOINT ["/usr/bin/selenwright", "-listen", ":4444", "-video-output-dir", "/opt/selenwright/video/"]
