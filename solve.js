#!/usr/bin/env node

/**
 * Simple client for POST /api/solve.
 */

const endpoint = "https://akira-turnstile.navierr.dev/api/solve";
const url = "https://154.26.137.28/";
const sitekey = "0x4AAAAAACNc_IrJs8GpsU_b";

const body = {
  url,
  sitekey,
};

try {
  const response = await fetch(endpoint, {
    method: "POST",
    headers: {
      "content-type": "application/json",
    },
    body: JSON.stringify(body),
  });

  const text = await response.text();
  let data;
  try {
    data = JSON.parse(text);
  } catch {
    data = text;
  }

  if (!response.ok) {
    console.error(`HTTP ${response.status}`);
    console.error(
      typeof data === "string" ? data : JSON.stringify(data, null, 2),
    );
    process.exit(1);
  }

  console.log(JSON.stringify(data, null, 2));
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
}
