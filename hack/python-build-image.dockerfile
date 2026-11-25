# This Dockerfile is used to build the build image used by Python functions. It
# is based on the upstream Debian image and adds the Python toolchain. We build
# and host this image so that project builds don't need to construct the build
# image before building functions.
#
# The image is built and pushed by the python-build-image action whenever this
# file changes on main. The base image is updated by renovate whenever Debian
# publishes a new 13-slim image.

FROM docker.io/library/debian:13-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0

RUN apt-get update && \
    apt-get install -y --no-install-recommends python3 python3-venv && \
    python3 -m venv /build && \
    /build/bin/pip install --quiet hatch && \
    rm -rf /var/lib/apt/lists/*
