const fs = require('fs');
const path = require('path');
const sharp = require('C:/Users/Amirs/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/sharp');

async function renderSvg(svgPath, pngPath, size = 2048) {
  const source = fs.readFileSync(svgPath);
  await sharp(source, { density: 600 })
    .resize(size, size, { fit: 'contain' })
    .png({ compressionLevel: 9 })
    .toFile(pngPath);
}

function fullBackgroundSvg(mode, size) {
  const fill = mode === 'light' ? '#FFFFFF' : 'url(#dark-bg)';
  return Buffer.from(`
    <svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}">
      <defs>
        <linearGradient id="dark-bg" x1="0" y1="0" x2="${size}" y2="${size}" gradientUnits="userSpaceOnUse">
          <stop offset="0" stop-color="#0F172A"/>
          <stop offset="1" stop-color="#06152B"/>
        </linearGradient>
      </defs>
      <rect width="100%" height="100%" fill="${fill}"/>
    </svg>
  `);
}

async function upgradeFullLockup(output, mode) {
  const fullSize = 4096;
  const symbolLeft = Math.round((124 / 504) * fullSize);
  const symbolTop = Math.round((15 / 504) * fullSize);
  const symbolSize = Math.round((260 / 504) * fullSize);
  const fullDir = path.join(output, 'png', 'full-lockup');
  const svgDir = path.join(output, 'svg', 'symbol');
  const transparentPath = path.join(
    fullDir,
    `hamlaneh-full-${mode}-transparent-4096.png`,
  );
  const backgroundPath = path.join(
    fullDir,
    `hamlaneh-full-${mode}-background-4096.png`,
  );
  const symbolSvg = path.join(
    svgDir,
    `hamlaneh-symbol-${mode}-transparent.svg`,
  );

  const eraser = await sharp({
    create: {
      width: symbolSize,
      height: symbolSize,
      channels: 4,
      background: { r: 255, g: 255, b: 255, alpha: 1 },
    },
  }).png().toBuffer();
  const cleanSymbol = await sharp(fs.readFileSync(symbolSvg), { density: 600 })
    .resize(symbolSize, symbolSize, { fit: 'fill' })
    .png()
    .toBuffer();
  const transparent = await sharp(transparentPath)
    .ensureAlpha()
    .composite([
      { input: eraser, left: symbolLeft, top: symbolTop, blend: 'dest-out' },
      { input: cleanSymbol, left: symbolLeft, top: symbolTop, blend: 'over' },
    ])
    .png({ compressionLevel: 9 })
    .toBuffer();

  await sharp(transparent)
    .withMetadata({ density: 300 })
    .png({ compressionLevel: 9 })
    .toFile(transparentPath);
  await sharp(fullBackgroundSvg(mode, fullSize), { density: 300 })
    .composite([{ input: transparent, left: 0, top: 0 }])
    .withMetadata({ density: 300 })
    .png({ compressionLevel: 9 })
    .toFile(backgroundPath);
}

function labelSvg(text, width) {
  return Buffer.from(`
    <svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="70">
      <rect width="100%" height="100%" fill="rgba(255,255,255,0.92)"/>
      <text x="32" y="47" font-family="Arial, sans-serif" font-size="30" font-weight="700" fill="#0F172A">${text}</text>
    </svg>
  `);
}

async function makeTile(imagePath, label, size = 1000, background = '#FFFFFF') {
  const art = await sharp(imagePath).resize(size, size, { fit: 'contain' }).png().toBuffer();
  return sharp({
    create: { width: size, height: size, channels: 4, background },
  })
    .composite([
      { input: art, left: 0, top: 0 },
      { input: labelSvg(label, size), left: 0, top: 0 },
    ])
    .png()
    .toBuffer();
}

async function main() {
  const output = process.argv[2];
  const symbolOnly = process.argv.includes('--symbol-only');
  if (!output) {
    throw new Error('usage: render_hamlaneh_logo_kit.cjs OUTPUT_DIR [--symbol-only]');
  }

  const svgDir = path.join(output, 'svg', 'symbol');
  const flatSvgDir = path.join(output, 'svg', 'symbol-flat');
  const pngDir = path.join(output, 'png', 'symbol');
  const flatPngDir = path.join(output, 'png', 'symbol-flat');
  const previewDir = path.join(output, 'preview');
  fs.mkdirSync(pngDir, { recursive: true });
  fs.mkdirSync(flatPngDir, { recursive: true });
  fs.mkdirSync(previewDir, { recursive: true });

  const variants = [
    ['hamlaneh-symbol-light-transparent.svg', 'hamlaneh-symbol-light-transparent-2048.png'],
    ['hamlaneh-symbol-light-background.svg', 'hamlaneh-symbol-light-background-2048.png'],
    ['hamlaneh-symbol-dark-transparent.svg', 'hamlaneh-symbol-dark-transparent-2048.png'],
    ['hamlaneh-symbol-dark-background.svg', 'hamlaneh-symbol-dark-background-2048.png'],
  ];

  for (const [svgName, pngName] of variants) {
    await renderSvg(path.join(svgDir, svgName), path.join(pngDir, pngName));
    await renderSvg(path.join(flatSvgDir, svgName), path.join(flatPngDir, pngName));
  }

  if (symbolOnly) {
    const tileSize = 520;
    const tiles = await Promise.all([
      makeTile(path.join(pngDir, variants[0][1]), 'GRADIENT · LIGHT · TRANSPARENT', tileSize, '#F7F6F2'),
      makeTile(path.join(pngDir, variants[1][1]), 'GRADIENT · LIGHT · TILE', tileSize, '#D7DEDA'),
      makeTile(path.join(pngDir, variants[2][1]), 'GRADIENT · DARK · TRANSPARENT', tileSize, '#111615'),
      makeTile(path.join(pngDir, variants[3][1]), 'GRADIENT · DARK · TILE', tileSize, '#D7DEDA'),
      makeTile(path.join(flatPngDir, variants[0][1]), 'FLAT · LIGHT · TRANSPARENT', tileSize, '#F7F6F2'),
      makeTile(path.join(flatPngDir, variants[1][1]), 'FLAT · LIGHT · TILE', tileSize, '#D7DEDA'),
      makeTile(path.join(flatPngDir, variants[2][1]), 'FLAT · DARK · TRANSPARENT', tileSize, '#111615'),
      makeTile(path.join(flatPngDir, variants[3][1]), 'FLAT · DARK · TILE', tileSize, '#D7DEDA'),
    ]);

    await sharp({
      create: { width: 2160, height: 1120, channels: 4, background: '#E5E7EB' },
    })
      .composite(tiles.map((input, index) => ({
        input,
        left: 40 + (index % 4) * tileSize,
        top: 40 + Math.floor(index / 4) * tileSize,
      })))
      .png({ compressionLevel: 9 })
      .toFile(path.join(previewDir, 'hamlaneh-symbol-quiet-nest-preview.png'));

    console.log(`Symbol PNG variants: ${variants.length * 2} (gradient + flat)`);
    console.log('Preview: hamlaneh-symbol-quiet-nest-preview.png');
    return;
  }

  await upgradeFullLockup(output, 'light');
  await upgradeFullLockup(output, 'dark');

  const fullDir = path.join(output, 'png', 'full-lockup');
  const tiles = await Promise.all([
    makeTile(path.join(fullDir, 'hamlaneh-full-light-background-4096.png'), 'FULL LOCKUP · LIGHT'),
    makeTile(path.join(fullDir, 'hamlaneh-full-dark-background-4096.png'), 'FULL LOCKUP · DARK'),
    makeTile(path.join(pngDir, 'hamlaneh-symbol-light-background-2048.png'), 'SYMBOL · LIGHT'),
    makeTile(path.join(pngDir, 'hamlaneh-symbol-dark-background-2048.png'), 'SYMBOL · DARK'),
  ]);

  await sharp({
    create: { width: 2080, height: 2080, channels: 4, background: '#E5E7EB' },
  })
    .composite([
      { input: tiles[0], left: 40, top: 40 },
      { input: tiles[1], left: 1040, top: 40 },
      { input: tiles[2], left: 40, top: 1040 },
      { input: tiles[3], left: 1040, top: 1040 },
    ])
    .png({ compressionLevel: 9 })
    .toFile(path.join(previewDir, 'hamlaneh-logo-kit-preview.png'));

  console.log(`Symbol PNG variants: ${variants.length * 2} (gradient + flat)`);
  console.log('Preview: hamlaneh-logo-kit-preview.png');
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
