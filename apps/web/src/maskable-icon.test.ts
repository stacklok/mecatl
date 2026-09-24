// SPDX-License-Identifier: Apache-2.0

import { readFile } from "node:fs/promises";
import { inflateSync } from "node:zlib";
import { expect, it } from "vitest";

function paeth(left: number, above: number, upperLeft: number) {
  const target = left + above - upperLeft;
  const leftDistance = Math.abs(target - left);
  const aboveDistance = Math.abs(target - above);
  const upperLeftDistance = Math.abs(target - upperLeft);
  if (leftDistance <= aboveDistance && leftDistance <= upperLeftDistance) return left;
  if (aboveDistance <= upperLeftDistance) return above;
  return upperLeft;
}

function rgbaPixels(png: Buffer) {
  const width = png.readUInt32BE(16);
  const height = png.readUInt32BE(20);
  expect(png[24]).toBe(8); // 8-bit channels
  expect(png[25]).toBe(6); // RGBA
  expect(png[28]).toBe(0); // no interlace

  const chunks: Buffer[] = [];
  for (let offset = 8; offset < png.length; ) {
    const length = png.readUInt32BE(offset);
    if (png.toString("ascii", offset + 4, offset + 8) === "IDAT") {
      chunks.push(png.subarray(offset + 8, offset + 8 + length));
    }
    offset += length + 12;
  }
  const rows = inflateSync(Buffer.concat(chunks));
  const stride = width * 4;
  const pixels = Buffer.alloc(stride * height);
  let read = 0;
  for (let y = 0; y < height; y++) {
    const filter = rows[read++];
    expect(filter).toBeGreaterThanOrEqual(0);
    expect(filter).toBeLessThanOrEqual(4);
    for (let x = 0; x < stride; x++) {
      const position = y * stride + x;
      const left = x >= 4 ? pixels[position - 4] : 0;
      const above = y > 0 ? pixels[position - stride] : 0;
      const upperLeft = y > 0 && x >= 4 ? pixels[position - stride - 4] : 0;
      const predictor = [
        0,
        left,
        above,
        Math.floor((left + above) / 2),
        paeth(left, above, upperLeft),
      ][filter];
      pixels[position] = (rows[read++] + predictor) & 255;
    }
  }
  expect(read).toBe(rows.length);
  return { height, pixels, width };
}

it("keeps the maskable mark inside the central safe circle", async () => {
  const png = await readFile(new URL("../public/icon-maskable-512.png", import.meta.url));
  const { height, pixels, width } = rgbaPixels(png);
  expect([width, height]).toEqual([512, 512]);
  const background = pixels.subarray(0, 4);
  expect(background[3]).toBe(255);

  const radiusSquared = (width * 0.4) ** 2;
  let outsideMark: string | undefined;
  for (let y = 0; y < height && !outsideMark; y++) {
    for (let x = 0; x < width; x++) {
      if ((x + 0.5 - width / 2) ** 2 + (y + 0.5 - height / 2) ** 2 <= radiusSquared) {
        continue;
      }
      const position = (y * width + x) * 4;
      if (
        pixels
          .subarray(position, position + 4)
          .some((channel, index) => channel !== background[index])
      ) {
        outsideMark = `${x},${y}`;
        break;
      }
    }
  }
  expect(outsideMark).toBeUndefined();
});
