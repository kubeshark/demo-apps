# Build context: repository root.
FROM node:22-slim@sha256:de170b39439eb0e5a3444ad7d60b015614a381e10fecae2e30ec9e735122f38d
WORKDIR /app
COPY clients/node/package.json clients/node/package-lock.json ./
RUN npm ci --omit=dev
COPY clients/node/client.mjs clients/markers.tsv ./
COPY internal/flows/inventory/markers.tsv ./inventory-markers.tsv
COPY proto ./proto
USER 65532
ENTRYPOINT ["node", "/app/client.mjs"]
