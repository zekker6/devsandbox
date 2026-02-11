FROM alpine:latest
RUN apk add --no-cache \
    meson gcc musl-dev libcap-dev libcap-static linux-headers git \
    make coreutils
