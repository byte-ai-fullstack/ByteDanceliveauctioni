FROM docker.elastic.co/elasticsearch/elasticsearch:8.19.17@sha256:aa6ee0ea2d708cea22a24ed44a420c5bbde9853d58a872e1e870f2086d04f652

# analysis-ik must exactly match the Elasticsearch runtime version.
ADD --chown=elasticsearch:root --chmod=0644 \
    --checksum=sha256:08d38d6d46ba309f23f0a45571a4d4b4a087f3e2319f51d7ff4617013d6a95b3 \
    https://release.infinilabs.com/analysis-ik/stable/elasticsearch-analysis-ik-8.19.17.zip \
    /tmp/elasticsearch-analysis-ik.zip

RUN bin/elasticsearch-plugin install --batch file:///tmp/elasticsearch-analysis-ik.zip \
    && bin/elasticsearch-plugin list | grep -Fx analysis-ik \
    && rm -f /tmp/elasticsearch-analysis-ik.zip
