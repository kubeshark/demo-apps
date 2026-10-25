// Calls a flow through the gateway over HTTPS and the inventory gRPC API over TLS on an
// interval, so TLS from the Node runtime shows up next to Go, the JVM and Python.
import { readFileSync } from "node:fs";
import https from "node:https";
import grpc from "@grpc/grpc-js";
import protoLoader from "@grpc/proto-loader";

// Values keyed by FIELD-REST of KSKEEP-<FLOW>-<FIELD>-<REST>.
function markers(path) {
  const out = {};
  for (const line of readFileSync(path, "utf8").split("\n")) {
    const cols = line.split("\t");
    if (cols.length < 3) continue;
    const parts = cols[1].split("-");
    if (parts.length < 4) continue;
    out[`${parts[2]}-${parts.slice(3).join("-")}`] = cols[1];
  }
  return out;
}

function interval(text) {
  const m = /^(\d+)(ms|s|m)$/.exec(text);
  if (!m) throw new Error(`bad INTERVAL ${text}`);
  return Number(m[1]) * { ms: 1, s: 1000, m: 60000 }[m[2]];
}

const log = (step, msg) => console.log(`${new Date().toISOString()} ${step} ${msg}`);

const own = markers("/app/markers.tsv");
const skus = Object.entries(markers("/app/inventory-markers.tsv"))
  .filter(([k]) => k.startsWith("SKU-"))
  .map(([, v]) => v);
const agent = own["AGENT-NODE"];
const caFile = process.env.CA_FILE ?? "/etc/demo-apps/tls/ca.crt";
const ca = readFileSync(caFile);
const gateway = new URL(process.env.GATEWAY_URL ?? "https://demo-apps-gateway");
const flow = process.env.FLOW ?? "graphql";
const every = interval(process.env.INTERVAL ?? "15s");
const inventoryAddr = process.env.INVENTORY_TLS_ADDR ?? "demo-apps-inventory:9443";

const def = protoLoader.loadSync("/app/proto/inventory/v1/inventory.proto", { keepCase: false });
const { InventoryService } = grpc.loadPackageDefinition(def).inventory.v1;

// A new HTTPS agent and gRPC channel each run, so the TLS handshake recurs.
function callGateway() {
  return new Promise((resolve) => {
    const req = https.request(
      {
        host: gateway.hostname,
        port: gateway.port || 443,
        path: `/api/flows/${flow}/run?source=node`,
        method: "POST",
        ca,
        agent: new https.Agent({ keepAlive: false }),
        headers: { "X-Demo-Client": agent },
        timeout: 30000,
      },
      (res) => {
        res.resume();
        res.on("end", () => resolve(`flow=${flow} status=${res.statusCode}`));
      },
    );
    req.on("error", (e) => resolve(`error=${e.message}`));
    req.on("timeout", () => req.destroy(new Error("timeout")));
    req.end();
  });
}

function getStock(sku) {
  return new Promise((resolve) => {
    const client = new InventoryService(inventoryAddr, grpc.credentials.createSsl(ca));
    const md = new grpc.Metadata();
    md.set("x-demo-client", agent);
    client.getStock({ sku }, md, { deadline: Date.now() + 10000 }, (err, resp) => {
      client.close();
      resolve(err ? `error=${err.message}` : `${resp.name}: ${resp.available} at ${resp.warehouse}`);
    });
  });
}

for (let n = 0; ; n++) {
  log("https", await callGateway());
  log("grpc", await getStock(skus[n % skus.length]));
  await new Promise((r) => setTimeout(r, every));
}
