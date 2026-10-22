#!/bin/sh
set -eu

image="${NOISE_BROWSER_TEST_IMAGE:-noise-browser-acceptance:local}"
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(dirname -- "${script_dir}")

if test "${NOISE_BROWSER_SKIP_BUILD:-0}" = 1; then
  docker image inspect "${image}" >/dev/null
  test "$(docker image inspect "${image}" --format '{{.Os}}/{{.Architecture}}')" = linux/amd64
else
  docker build --target browser --tag "${image}" "${repo_dir}"
fi
docker run --rm --init \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,exec,nosuid,nodev,size=512m \
  --entrypoint /bin/sh \
  "${image}" -c '
    set -eu
    node /opt/noise-browser/server.mjs >/tmp/server.log 2>&1 &
    server_pid=$!
    trap '\''kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true'\'' EXIT
    node - <<'\''NODE'\''
const { chromium } = require("/opt/noise-browser/node_modules/playwright");

async function connect() {
  let lastError;
  for (let attempt = 0; attempt < 50; attempt++) {
    try {
      return await chromium.connect("ws://127.0.0.1:3000/");
    } catch (error) {
      lastError = error;
      await new Promise(resolve => setTimeout(resolve, 100));
    }
  }
  throw lastError;
}

(async () => {
  const browser = await connect();
  const page = await browser.newPage();
  await page.setContent("<title>Noise browser acceptance</title>");
  if (await page.title() !== "Noise browser acceptance")
    throw new Error("Chromium did not render the acceptance page");
  await browser.close();
})().catch(error => {
  console.error(error);
  process.exit(1);
});
NODE
  '
