# Build context: repository root.
FROM python:3.12-slim@sha256:a0f57064cbdcd6285fa45f51237ab0b35ea8ebaed06a1f5973e89e19dfb86704
# psycopg's pure-Python build loads the system libpq, which links the system libssl that
# the tracer hooks; the binary wheel would bundle its own copies.
RUN apt-get update \
 && apt-get install -y --no-install-recommends libpq5 \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY clients/python/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY clients/python/client.py clients/markers.tsv /app/
USER 65532
ENTRYPOINT ["python", "-u", "/app/client.py"]
