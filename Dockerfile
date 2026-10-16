FROM debian:bookworm-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates \
  && rm -rf /var/lib/apt/lists/*
COPY bloomary-api /usr/local/bin/bloomary-api
EXPOSE 4178 4444
USER 65534:65534
ENTRYPOINT ["/usr/local/bin/bloomary-api"]
