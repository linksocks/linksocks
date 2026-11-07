import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

// Run explicitly as root against a local Worker listening on 0.0.0.0:8788.
// This test uses real L2 TAP interfaces (--vlan) in two isolated network
// namespaces.
assert.equal(process.getuid(), 0, "run this integration test with sudo");
const password = process.env.LINKSOCKS_ZONE_PASSWORD;
assert.ok(password, "set LINKSOCKS_ZONE_PASSWORD to the local administrator password");
const binary = process.env.LINKSOCKS_BINARY || "/tmp/opencode/linksocks-zone";
const origin = process.env.LINKSOCKS_ZONE_URL || "http://127.0.0.1:8788";
const port = new URL(origin).port || "80";
const tag = String(process.pid);
const subnet = 10 + process.pid % 200;
const nsA = `lzt-a-${tag}`, nsB = `lzt-b-${tag}`;
const hostA = `lza${tag}`, hostB = `lzb${tag}`;
const ipA = `198.18.${subnet}.1`, ipB = `198.18.${subnet}.5`;
const directory = await mkdtemp(join(process.env.TMPDIR || "/tmp/opencode", "linksocks-zone-e2e-"));
const children = [];
const namespaces = [];
const firewallRules = [];
const zoneCIDR = "10.249.231.0/29";
let cookie, csrf, zoneID;

function ip(...args) {
  return execFileSync("ip", args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
}

async function api(path, init = {}) {
  const response = await fetch(origin + path, {
    ...init,
    headers: { Cookie: cookie || "", "X-CSRF-Token": csrf || "", "Content-Type": "application/json", ...init.headers },
  });
  const body = await response.json();
  assert.ok(response.ok, `${path}: ${response.status}: ${JSON.stringify(body)}`);
  return body;
}

function namespace(name, host, address, peer) {
  ip("netns", "add", name);
  namespaces.push(name);
  ip("link", "add", host, "type", "veth", "peer", "name", "eth0", "netns", name);
  ip("address", "add", `${address}/30`, "dev", host);
  ip("link", "set", host, "up");
  ip("-n", name, "link", "set", "lo", "up");
  ip("-n", name, "address", "add", `${peer}/30`, "dev", "eth0");
  ip("-n", name, "link", "set", "eth0", "up");
  const rule = ["INPUT", "-i", host, "-s", peer, "-d", address, "-p", "tcp", "--dport", port, "-j", "ACCEPT"];
  try {
    execFileSync("iptables", ["-I", ...rule]);
    firewallRules.push(rule);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
}

function start(ns, command, args, readyText) {
  const child = spawn("ip", ["netns", "exec", ns, command, ...args], { stdio: ["ignore", "pipe", "pipe"] });
  children.push(child);
  let log = "";
  child.done = new Promise(resolve => child.once("exit", (code, signal) => resolve({ code, signal })));
  child.ready = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`startup timeout: ${log}`)), 20000);
    const collect = chunk => {
      const text = chunk.toString();
      log += text;
      process.stdout.write(`[${ns}] ${text}`);
      if (log.includes(readyText)) { clearTimeout(timer); resolve(); }
    };
    child.stdout.on("data", collect);
    child.stderr.on("data", collect);
    child.once("error", error => { clearTimeout(timer); reject(error); });
    child.once("exit", code => { clearTimeout(timer); reject(new Error(`process exited (${code}): ${log}`)); });
  });
  return child;
}

function joinMachine(ns, host, identity, name) {
  return start(ns, binary, ["zone", "join", zoneID, "--url", `http://${host}:${port}`, "--identity", identity, "--name", name, "--vlan", "-E"], "Joined ");
}

async function until(predicate, description) {
  for (let i = 0; i < 100; i++) {
    if (await predicate()) return;
    await delay(100);
  }
  throw new Error(`timeout: ${description}`);
}

try {
  const login = await fetch(origin + "/admin?login", {
    method: "POST", redirect: "manual",
    body: new URLSearchParams({ username: "admin", password }),
  });
  assert.equal(login.status, 303);
  cookie = login.headers.get("set-cookie")?.split(";", 1)[0];
  assert.ok(cookie?.startsWith("ls_session="));
  const page = await (await fetch(origin + "/admin?tab=settings", { headers: { Cookie: cookie } })).text();
  csrf = page.match(/name="csrf" value="([^"]+)"/)?.[1];
  assert.ok(csrf);
  const created = await api("/api/zones", { method: "POST", body: JSON.stringify({ name: "Zone network test", cidr: zoneCIDR, max_members: 6 }) });
  zoneID = created.zone.id;
  console.log(`Created zone ${zoneID} (${zoneCIDR}).`);
  console.log(execFileSync(binary, ["zone", "list", "--url", origin, "-E"], { encoding: "utf8" }).trim());

  namespace(nsA, hostA, ipA, `198.18.${subnet}.2`);
  namespace(nsB, hostB, ipB, `198.18.${subnet}.6`);
  const keyA = join(directory, "a.key"), keyB = join(directory, "b.key");
  const a = joinMachine(nsA, ipA, keyA, "machine-a");
  await a.ready;
  let b = joinMachine(nsB, ipB, keyB, "machine-b");
  await b.ready;
  await until(async () => (await api(`/api/zones/${zoneID}/info`)).members.filter(m => m.online).length === 2, "two online members");
  const roster = await api(`/api/zones/${zoneID}/info`);
  const memberA = roster.members.find(m => m.name === "machine-a");
  const memberB = roster.members.find(m => m.name === "machine-b");
  assert.ok(memberA && memberB);
  console.log(`Assigned ${memberA.ip} and ${memberB.ip}.`);
  console.log(ip("netns", "exec", nsA, "ping", "-c", "3", "-W", "3", "-s", "1472", memberB.ip));
  console.log(ip("netns", "exec", nsB, "ping", "-c", "1", "-W", "3", memberA.ip));

  const server = start(nsB, process.execPath, ["-e", `
    const http = require('node:http'), dgram = require('node:dgram');
    http.createServer((req,res) => res.end(Buffer.alloc(256*1024, 90))).listen(18080, '0.0.0.0', () => console.log('server ready'));
    const udp = dgram.createSocket('udp4');
    udp.on('message', (data, peer) => udp.send(data, peer.port, peer.address));
    udp.bind(18081, '0.0.0.0');
  `], "server ready");
  await server.ready;
  const expected = createHash("sha256").update(Buffer.alloc(256 * 1024, 90)).digest("hex");
  const tcp = execFileSync("ip", ["netns", "exec", nsA, process.execPath, "-e", `
    const http=require('node:http'), crypto=require('node:crypto');
    const request = http.get('http://${memberB.ip}:18080/', response => {
      const chunks=[]; response.on('data', c => chunks.push(c));
      response.on('end', () => {
        const body=Buffer.concat(chunks), sum=crypto.createHash('sha256').update(body).digest('hex');
        if(body.length!==262144 || sum!=='${expected}') process.exit(1);
        console.log('TCP: '+body.length+' bytes, SHA-256 '+sum);
      });
    });
    request.setTimeout(10000, () => { request.destroy(); process.exit(1); });
    request.on('error', error => { console.error(error); process.exit(1); });
  `], { encoding: "utf8", timeout: 15000 });
  console.log(tcp.trim());
  const udp = execFileSync("ip", ["netns", "exec", nsA, process.execPath, "-e", `
    const dgram=require('node:dgram'), crypto=require('node:crypto');
    const socket=dgram.createSocket('udp4'), payload=crypto.randomBytes(1200);
    const timer=setTimeout(() => process.exit(1),5000);
    socket.on('message', data => {
      if(!data.equals(payload)) process.exit(1);
      clearTimeout(timer); socket.close(); console.log('UDP: 1200-byte echo verified.');
    });
    socket.send(payload,18081,'${memberB.ip}');
  `], { encoding: "utf8", timeout: 10000 });
  console.log(udp.trim());

  b.kill("SIGKILL");
  await b.done;
  await until(async () => !(await api(`/api/zones/${zoneID}/info`)).members.find(m => m.identity === memberB.identity)?.online, "offline lease");
  b = joinMachine(nsB, ipB, keyB, "machine-b");
  await b.ready;
  const rejoined = (await api(`/api/zones/${zoneID}/info`)).members.find(m => m.identity === memberB.identity);
  assert.equal(rejoined.ip, memberB.ip);
  console.log(`Rejoin retained ${rejoined.ip}.`);
  console.log(ip("netns", "exec", nsA, "ping", "-c", "1", "-W", "3", memberB.ip));

  await api(`/api/zones/${zoneID}/settings`, {
    method: "POST",
    body: JSON.stringify({
      welcome: "", default_action: "allow",
      rules: [{ src: "any", src_port: "any", proto: "icmp", dst: "any", dst_port: "any", action: "deny" }],
    }),
  });
  await delay(700);
  let denied = "";
  try {
    denied = ip("netns", "exec", nsA, "ping", "-c", "1", "-W", "2", memberB.ip);
  } catch (error) {
    denied = error.stdout || "";
  }
  assert.match(denied, /100% packet loss/, "an ICMP deny rule must stop pings");
  console.log("Firewall: an ICMP deny rule stopped pings in VLAN mode.");
  await api(`/api/zones/${zoneID}/settings`, {
    method: "POST",
    body: JSON.stringify({ welcome: "", default_action: "allow", rules: [] }),
  });
  await delay(700);
  console.log(ip("netns", "exec", nsA, "ping", "-c", "1", "-W", "3", memberB.ip));
  console.log("Firewall: clearing the rule restored pings.");

  console.log(ip("netns", "exec", nsB, binary, "zone", "leave", zoneID, "--url", `http://${ipB}:${port}`, "--identity", keyB, "-E"));
  await until(async () => !(await api(`/api/zones/${zoneID}/info`)).members.some(m => m.identity === memberB.identity), "explicit leave releases lease");
  assert.equal((await b.done).code, 0, "remote leave should stop the active client cleanly");
  console.log("Leave disconnected the running machine and released its IPv4 lease.");

  a.kill("SIGTERM");
  const stopped = await a.done;
  assert.equal(stopped.code, 0, "Ctrl+C-style shutdown should leave cleanly");
  await until(async () => (await api(`/api/zones/${zoneID}/info`)).members.length === 0, "signal-driven leave");
  console.log("Signal-driven leave removed the final membership.");
  console.log("PASS: real TAP Ethernet (ARP), ICMP, TCP, UDP, reconnect and leave.");
} finally {
  for (const child of children) {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
  }
  await Promise.all(children.map(child => child.done));
  for (const rule of firewallRules) {
    try { execFileSync("iptables", ["-D", ...rule]); } catch (error) { console.error(error.message); }
  }
  for (const name of namespaces.reverse()) {
    try { ip("netns", "delete", name); } catch (error) { console.error(error.message); }
  }
  if (zoneID) {
    try { await api(`/api/zones/${zoneID}`, { method: "DELETE" }); } catch (error) { console.error(error.message); }
  }
  await rm(directory, { recursive: true, force: true });
}
