# syntax=docker/dockerfile:1
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
        git \
        libffi-dev \
        libxml2-dev \
        libxslt1-dev \
        nodejs \
        npm \
        ruby \
        ruby-bigdecimal \
        ruby-dev \
        tini \
        zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

RUN gem install nokogiri --version 1.18.6 --no-document -- --use-system-libraries
RUN gem install html-proofer --version 5.0.10 --no-document -- --use-system-libraries

RUN npm install --global \
    markdownlint@0.29.0 \
    markdownlint-cli@0.35.0

COPY ./scripts/verify.sh /verify.sh
COPY ./scripts/lint.sh /lint.sh
COPY ./scripts/lint-yaml.sh /lint-yaml.sh

WORKDIR /app
VOLUME ["/tmp","/app"]

# Tini propagates termination to the parallel check subprocesses.
ENTRYPOINT ["/usr/bin/tini","-g","--","sh"]
