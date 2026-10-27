// Mirrors internal/protocol. Keep both in sync.
const WRITE_CHAR = "0000fff2-0000-1000-8000-00805f9b34fb";
const SERVICE = 0xfff0; // TODO: verify the DFT200 service UUID
const CMD = { start: 0x01, stop: 0x02, speed: 0x03, pause: 0x04 };

function encode(cmd, value = 0) {
  const p = [0xf0, 0xc3, 0x03, cmd, value, 0x00];
  p.push(p.reduce((a, b) => (a + b) & 0xff, 0));
  return new Uint8Array(p);
}

let writeChar;
const status = document.getElementById("status");

document.getElementById("connect").onclick = async () => {
  try {
    const dev = await navigator.bluetooth.requestDevice({ acceptAllDevices: true, optionalServices: [SERVICE] });
    const gatt = await dev.gatt.connect();
    const service = await gatt.getPrimaryService(SERVICE);
    writeChar = await service.getCharacteristic(WRITE_CHAR);
    status.textContent = `Connected to ${dev.name || dev.id}`;
  } catch (err) {
    status.textContent = `Connect failed: ${err.message}`;
  }
};

async function send(action, value = 0) {
  if (!writeChar) { status.textContent = "Connect first"; return; }
  const raw = action === "speed" ? value * 10 : 0;
  await writeChar.writeValueWithoutResponse(encode(CMD[action], raw));
  // TODO: queue in localStorage when the server is unreachable
  await fetch("/api/events", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify([{ id: crypto.randomUUID(), device: "web", action, value, ts: new Date().toISOString() }]),
  });
  status.textContent = action === "speed" ? `Speed ${value}` : action;
}

document.querySelectorAll("[data-action]").forEach(b => b.onclick = () => send(b.dataset.action));
const speeds = document.getElementById("speeds");
for (let level = 1; level <= 8; level++) {
  const b = document.createElement("button");
  b.textContent = level;
  b.onclick = () => send("speed", level);
  speeds.append(b);
}
