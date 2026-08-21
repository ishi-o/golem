# Runtime image for the Docker and Kubernetes sandbox adapters.
# The adapters replace the default command with their watchdog loop.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install --no-install-recommends --yes \
        bash \
        ca-certificates \
        coreutils \
        util-linux \
    && rm -rf /var/lib/apt/lists/*

ENV HOME=/root
WORKDIR /root
CMD ["sleep", "infinity"]
