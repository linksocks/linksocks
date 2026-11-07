import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:http";
import { createSocket } from "node:dgram";
import net from "node:net";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

// Proxy-mode end-to-end test: no root, no interfaces. Two members run on this
// machine against a local Worker (default http://127.0.0.1:8787, admin
// password dev-password). Member A reaches member B through its local SOCKS5
// proxy: TCP with curl --socks5-hostname and UDP with a raw SOCKS5 client.
const password = process.env.LINKSOCKS_ZONE_PASSWORD || "dev-password";
const binary = process.env.LINKSOCKS_BINARY || "/tmp/opencode/linksocks-zone";
const origin = process.env.LINKSOCKS_ZONE_URL || "http://127.0.0.1:8787";
const port = new URL(origin).port || "80";
const socksPort = 19000 + (process.pid % 2000);
const httpPort = 24000 + (process.pid % 2000);
const udpPort = 26000 + (process.pid % 2000);
const zoneCIDR = `10.${100 + (process.pid % 120)}.${(process.pid * 7) % 250}.0/29`;
const directory = await mkdtemp(join(process.env.TMPDIR || "/tmp/opencode", "linksocks-zone-socks-"));
const children = [];
let cookie, csrf, zoneID;

async function api(path, init = {}) {
  const response = await fetch(origin + path, {
    ...init,
    headers: { Cookie: cookie || "", "X-CSRF-Token": csrf || "", "Content-Type": "application/json", ...init.headers },
  });
  const body = await response.json();
  assert.ok(response.ok, `${path}: ${response.status}: ${JSON.stringify(body)}`);
  return body;
}

function start(command, args, readyText) {
  const child = spawn(command, args, { stdio: ["ignore", "pipe", "pipe"] });
  children.push(child);
  let log = "";
  child.done = new Promise(resolve => child.once("exit", (code, signal) => resolve({ code, signal })));
  child.ready = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${command} startup timeout: ${log}`)), 30000);
    const collect = chunk => {
      const text = chunk.toString();
      log += text;
      process.stdout.write(`[zone] ${text}`);
      if (log.includes(readyText)) { clearTimeout(timer); resolve(); }
    };
    child.stdout.on("data", collect);
    child.stderr.on("data", collect);
    child.once("error", error => { clearTimeout(timer); reject(error); });
    child.once("exit", code => { clearTimeout(timer); reject(new Error(`${command} exited (${code}): ${log}`)); });
  });
  return child;
}

async function until(predicate, description) {
  for (let i = 0; i < 150; i++) {
    if (await predicate()) return;
    await delay(100);
  }
  throw new Error(`timeout: ${description}`);
}

function parseSocksReply(buffer) {
  assert.equal(buffer[0], 5, "SOCKS version");
  assert.equal(buffer[1], 0, `SOCKS reply code ${buffer[1]}`);
  assert.equal(buffer[3], 1, "SOCKS reply atyp");
  return { address: buffer.subarray(4, 8), port: buffer.readUInt16BE(8) };
}

// resolveSocks performs the SOCKS5 greeting and returns the relay socket.
function socksHandshake(host, proxyPort) {
  return new Promise((resolve, reject) => {
    const socket = net.connect(proxyPort, host);
    let stage = "greeting";
    const buffer = [];
    const timer = setTimeout(() => reject(new Error("SOCKS handshake timeout")), 10000);
    const finish = (error, value) => { clearTimeout(timer); error ? reject(error) : resolve(value); };
    socket.on("error", error => finish(error));
    let received = Buffer.alloc(0);
    socket.on("data", chunk => {
      received = Buffer.concat([received, chunk]);
      if (stage === "greeting" && received.length >= 2) {
        if (received[0] !== 5 || received[1] !== 0) return finish(new Error(`SOCKS greeting rejected: ${received.toString("hex")}`));
        received = received.subarray(2);
        stage = "request";
        socket.write(Buffer.from([5, 3, 0, 1, 0, 0, 0, 0, 0, 0]));
      }
      if (stage === "request" && received.length >= 10) {
        const reply = parseSocksReply(received);
        finish(null, { socket, relay: reply });
      }
    });
    socket.write(Buffer.from([5, 1, 0]));
  });
}

async function socksUDP(proxyPort, targetIP, targetPort, payload) {
  const { socket, relay } = await socksHandshake("127.0.0.1", proxyPort);
  const udp = createSocket("udp4");
  try {
    const header = Buffer.alloc(10);
    header[3] = 1;
    header.set(targetIP.split(".").map(Number), 4);
    header.writeUInt16BE(targetPort, 8);
    const response = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("SOCKS UDP echo timeout")), 10000);
      udp.on("message", message => {
        if (message[0] !== 0 || message[1] !== 0 || message[2] !== 0 || message[3] !== 1) return;
        clearTimeout(timer);
        resolve({ source: message.subarray(4, 8), port: message.readUInt16BE(8), payload: message.subarray(10) });
      });
      udp.on("error", error => { clearTimeout(timer); reject(error); });
      udp.send(Buffer.concat([header, payload]), relay.port, relay.address.join("."));
    });
    return response;
  } finally {
    udp.close();
    socket.destroy();
  }
}

// curl runs asynchronously: execFileSync would block this process's event loop,
// which also runs the member services the request has to reach.
function curl(target) {
  return new Promise((resolve, reject) => {
    execFile("curl", ["-sS", "--max-time", "20", "--socks5-hostname", `127.0.0.1:${socksPort}`, target], { encoding: "utf8" }, (error, stdout, stderr) => {
      if (error) reject(new Error(`curl ${target}: ${stderr || error.message}`));
      else resolve(stdout);
    });
  });
}

let httpServer, udpServer;
try {
  const login = await fetch(origin + "/admin?login", {
    method: "POST", redirect: "manual",
    body: new URLSearchParams({ username: "admin", password }),
  });
  assert.equal(login.status, 303, "admin login failed; is the local Worker running?");
  cookie = login.headers.get("set-cookie")?.split(";", 1)[0];
  assert.ok(cookie?.startsWith("ls_session="));
  const page = await (await fetch(origin + "/admin?tab=settings", { headers: { Cookie: cookie } })).text();
  csrf = page.match(/name="csrf" value="([^"]+)"/)?.[1];
  assert.ok(csrf, "missing CSRF token");
  const created = await api("/api/zones", { method: "POST", body: JSON.stringify({ name: `Proxy test ${process.pid}`, cidr: zoneCIDR, max_members: 4 }) });
  zoneID = created.zone.id;
  console.log(`Created private zone ${zoneID} (${zoneCIDR}).`);

  httpServer = createServer((request, response) => response.end("zone tcp answer"));
  await new Promise(resolve => httpServer.listen(httpPort, "127.0.0.1", resolve));
  udpServer = createSocket("udp4");
  udpServer.on("message", (data, peer) => udpServer.send(data, peer.port, peer.address));
  await new Promise(resolve => udpServer.bind(udpPort, "127.0.0.1", resolve));
  console.log(`Member B services on 127.0.0.1:${httpPort} (TCP) and 127.0.0.1:${udpPort} (UDP).`);

  const keyA = join(directory, "a.key"), keyB = join(directory, "b.key");
  const b = start(binary, ["zone", "join", zoneID, "--url", origin, "--identity", keyB, "--name", "machine-b", "--socks-port", "0", "-E"], "Joined ");
  await b.ready;
  const a = start(binary, ["zone", "join", zoneID, "--url", origin, "--identity", keyA, "--name", "machine-a", "--socks-port", String(socksPort), "-E"], `SOCKS5 proxy listening on 127.0.0.1:${socksPort}`);
  await a.ready;
  await until(async () => (await api(`/api/zones/${zoneID}/info`)).members.filter(m => m.online).length === 2, "two online members");
  const roster = await api(`/api/zones/${zoneID}/info`);
  const memberB = roster.members.find(m => m.name === "machine-b");
  assert.ok(memberB?.ip, "member B has no address");
  console.log(`Member B is ${memberB.ip}. Proxy is 127.0.0.1:${socksPort}.`);

  const body = await curl(`http://${memberB.ip}:${httpPort}/`);
  assert.equal(body, "zone tcp answer", "TCP response through the proxy");
  const named = await curl(`http://machine-b:${httpPort}/`);
  assert.equal(named, "zone tcp answer", "TCP response through the member name");
  console.log("TCP: curl --socks5-hostname reached machine-b by virtual IP and by name.");

  const payload = Buffer.from(`udp echo ${process.pid}`);
  const response = await socksUDP(socksPort, memberB.ip, udpPort, payload);
  assert.deepEqual(response.payload.subarray(0, payload.length), payload, "UDP echo payload");
  assert.deepEqual([...response.source], memberB.ip.split(".").map(Number), "UDP reply source address");
  assert.equal(response.port, udpPort, "UDP reply source port");
  console.log(`UDP: echoed ${payload.length} bytes through SOCKS5 UDP ASSOCIATE.`);

  await api(`/api/zones/${zoneID}/settings`, {
    method: "POST",
    body: JSON.stringify({
      welcome: "", default_action: "allow",
      rules: [{ src: "any", src_port: "any", proto: "tcp", dst: "any", dst_port: String(httpPort), action: "deny" }],
    }),
  });
  await until(async () => {
    const info = await api(`/api/zones/${zoneID}/info`);
    return info.settings.rules.length === 1;
  }, "settings visible through the zone API");
  await delay(500);
  let blocked = null;
  try {
    await curl(`http://${memberB.ip}:${httpPort}/`);
  } catch (error) {
    blocked = error.message;
  }
  assert.ok(blocked, "a denied TCP port must not connect through the proxy");
  console.log("Firewall: a deny rule stopped a new TCP connection through the proxy.");
  await api(`/api/zones/${zoneID}/settings`, {
    method: "POST",
    body: JSON.stringify({ welcome: "", default_action: "allow", rules: [] }),
  });
  await delay(500);
  assert.equal(await curl(`http://${memberB.ip}:${httpPort}/`), "zone tcp answer", "clearing the rule restores access");
  console.log("Firewall: clearing the rule restored access.");

  a.kill("SIGTERM");
  b.kill("SIGTERM");
  console.log("PASS: proxy-mode SOCKS5 TCP and UDP between two members.");
} catch (error) {
  console.error("Proxy-mode end-to-end test failed.");
  console.error("If the coordinator relay has not implemented zone v2 streams and datagrams yet, this is expected: waiting for the relay.");
  throw error;
} finally {
  for (const child of children) {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
  }
  await Promise.all(children.map(child => child.done));
  if (httpServer) httpServer.close();
  if (udpServer) udpServer.close();
  if (zoneID) {
    try { await api(`/api/zones/${zoneID}`, { method: "DELETE" }); } catch (error) { console.error(error.message); }
  }
  await rm(directory, { recursive: true, force: true });
}
