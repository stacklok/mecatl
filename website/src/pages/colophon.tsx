import React from 'react';
import Layout from '@theme/Layout';
import styles from './colophon.module.css';

// ─── Color swatches ──────────────────────────────────────────────────────────

const colors = [
  {name: '--mec-bg',           hex: '#141210', label: 'Background',         role: 'Page background'},
  {name: '--mec-surface',      hex: '#1e1b18', label: 'Surface',            role: 'Navbar, footer, cards'},
  {name: '--mec-surface-2',    hex: '#272320', label: 'Surface 2',          role: 'Code blocks, table headers'},
  {name: '--mec-teal',         hex: '#2a9d8f', label: 'Teal',               role: 'Primary accent — collar'},
  {name: '--mec-teal-bright',  hex: '#3dbdad', label: 'Teal (bright)',      role: 'Hover, active, h3'},
  {name: '--mec-teal-dim',     hex: '#1d7068', label: 'Teal (dim)',         role: 'Subtle borders, table rule'},
  {name: '--mec-copper',       hex: '#c87941', label: 'Copper',             role: 'Secondary accent — ears/tail'},
  {name: '--mec-copper-bright',hex: '#e08f52', label: 'Copper (bright)',    role: 'Hover'},
  {name: '--mec-rope',         hex: '#d4a76a', label: 'Rope',               role: 'Warm neutral, inline code'},
  {name: '--mec-text',         hex: '#f0ece8', label: 'Text',               role: 'Primary text'},
  {name: '--mec-text-muted',   hex: '#9e9690', label: 'Text (muted)',       role: 'Secondary text, subtitles'},
  {name: '--mec-border',       hex: '#332f2b', label: 'Border',             role: 'Dividers, card edges'},
];

// ─── Type specimens ───────────────────────────────────────────────────────────

const typefaces = [
  {
    family: 'Space Grotesk',
    role: 'Headings, navbar, labels, CTAs',
    source: 'Google Fonts',
    weights: ['300', '400', '500', '600', '700'],
    specimen: 'A cloud-native agentic harness',
    css: "'Space Grotesk', system-ui, sans-serif",
  },
  {
    family: 'Inter',
    role: 'Body text',
    source: 'Google Fonts',
    weights: ['400', '500'],
    specimen: 'The port model lets you swap any adapter without touching the engine loop.',
    css: "'Inter', system-ui, -apple-system, sans-serif",
  },
  {
    family: 'JetBrains Mono',
    role: 'Code, terminal output',
    source: 'Google Fonts',
    weights: ['400', '500'],
    specimen: 'go run ./cmd/mecademo',
    css: "'JetBrains Mono', 'SFMono-Regular', Menlo, monospace",
  },
];

// ─── Mascot note ─────────────────────────────────────────────────────────────

function Swatch({hex, label, name, role}: typeof colors[number]) {
  const isDark = (h: string) => {
    const r = parseInt(h.slice(1, 3), 16);
    const g = parseInt(h.slice(3, 5), 16);
    const b = parseInt(h.slice(5, 7), 16);
    return (r * 299 + g * 587 + b * 114) / 1000 < 128;
  };
  const textColor = isDark(hex) ? '#f0ece8' : '#141210';

  return (
    <div className={styles.swatch}>
      <div className={styles.swatchColor} style={{background: hex, color: textColor}}>
        {hex}
      </div>
      <div className={styles.swatchMeta}>
        <span className={styles.swatchLabel}>{label}</span>
        <code className={styles.swatchVar}>{name}</code>
        <span className={styles.swatchRole}>{role}</span>
      </div>
    </div>
  );
}

function TypeSpecimen({family, role, source, weights, specimen, css}: typeof typefaces[number]) {
  return (
    <div className={styles.typeCard}>
      <div className={styles.typeHeader}>
        <span className={styles.typeFamily}>{family}</span>
        <span className={styles.typeMeta}>{source} · {role}</span>
      </div>
      <div className={styles.typeSpecimen} style={{fontFamily: css}}>
        {specimen}
      </div>
      <div className={styles.typeWeights}>
        {weights.map(w => (
          <span key={w} className={styles.typeWeight} style={{fontFamily: css, fontWeight: parseInt(w)}}>
            {w}
          </span>
        ))}
      </div>
      <code className={styles.typeCss}>{css}</code>
    </div>
  );
}

export default function Colophon(): React.ReactElement {
  return (
    <Layout title="Colophon — mecatl brand reference" description="Brand tokens, typefaces, and design decisions for mecatl.dev">
      <main className={styles.colophon}>

        <section className={styles.section}>
          <h1 className={styles.pageTitle}>Colophon</h1>
          <p className={styles.pageSubtitle}>
            Brand reference for mecatl.dev — colors, typography, and design rationale.
            All tokens are CSS custom properties defined in <code>website/src/css/custom.css</code>.
          </p>
        </section>

        <section className={styles.section}>
          <h2>Mascot & origin</h2>
          <div className={styles.mascotSection}>
            <img
              src="/img/mecatito.png"
              alt="Mecatito — a Xoloitzcuintli puppy with a teal rope collar and copper ears, serving as the mecatl mascot"
              className={styles.mascotImg}
            />
            <div className={styles.mascotNote}>
              <p>
                <strong>Mecatito</strong> is a Xoloitzcuintli (Xolo) puppy — Mexico's ancient hairless dog,
                companion to the dead in Aztec mythology. The name <em>mecatl</em> is Nahuatl for "rope" or "cord",
                reflected in the rope collar and the project's rope-as-harness metaphor.
              </p>
              <p>
                The color palette is derived directly from the mascot illustration:
                the charcoal body becomes the background,
                the teal collar becomes the primary accent,
                and the copper ears and tail become the secondary accent.
                The rope provides the warm neutral.
              </p>
              <p>
                Created by Ozz. Source: <code>assets/mecatito.png</code>.
              </p>
            </div>
          </div>
        </section>

        <section className={styles.section}>
          <h2>Color tokens</h2>
          <p className={styles.sectionNote}>
            Dark-only site — one set of tokens, no light/dark split.
            All tokens are on <code>:root</code> and <code>[data-theme='dark']</code>.
          </p>
          <div className={styles.swatchGrid}>
            {colors.map(c => <Swatch key={c.name} {...c} />)}
          </div>
        </section>

        <section className={styles.section}>
          <h2>Typography</h2>
          <p className={styles.sectionNote}>
            All three families loaded from Google Fonts via a single <code>@import</code> in <code>custom.css</code>.
            No self-hosted font files required.
          </p>
          <div className={styles.typeGrid}>
            {typefaces.map(t => <TypeSpecimen key={t.family} {...t} />)}
          </div>
        </section>

        <section className={styles.section}>
          <h2>Type scale</h2>
          <div className={styles.scaleTable}>
            {[
              {el: 'h1', size: 'Home: clamp(2.2rem, 4vw, 3.4rem) / docs: 3rem desktop', weight: '700', font: 'Space Grotesk', usage: 'Page titles'},
              {el: 'h2', size: 'Home: clamp(1.85rem, 3vw, 2.45rem) / docs: 2rem desktop', weight: '600 or 700', font: 'Space Grotesk', usage: 'Section titles'},
              {el: 'h3', size: 'Home: 1rem / docs: 1.5rem desktop', weight: '600', font: 'Space Grotesk', usage: 'Subsections and card titles'},
              {el: 'body', size: '1rem (16px)', weight: '400', font: 'Inter', usage: 'All body copy'},
              {el: 'small / muted', size: '0.9rem', weight: '400', font: 'Inter', usage: 'Subtitles, card bodies'},
              {el: 'label / eyebrow', size: '0.7–0.75rem', weight: '600', font: 'Space Grotesk', usage: 'Uppercase labels, sidebar categories'},
              {el: 'code', size: '0.88em', weight: '400', font: 'JetBrains Mono', usage: 'Inline code, code blocks, terminal'},
            ].map(row => (
              <div key={row.el} className={styles.scaleRow}>
                <code className={styles.scaleEl}>{row.el}</code>
                <span className={styles.scaleSize}>{row.size}</span>
                <span className={styles.scaleFont}>{row.font} {row.weight}</span>
                <span className={styles.scaleUsage}>{row.usage}</span>
              </div>
            ))}
          </div>
        </section>

        <section className={styles.section}>
          <h2>UI specimens</h2>

          <h3>Buttons</h3>
          <div className={styles.specimenRow}>
            <a href="#" className="button button--primary" onClick={e => e.preventDefault()}>Read the docs</a>
            <a href="#" className={styles.ctaSecondarySpec} onClick={e => e.preventDefault()}>View on GitHub</a>
          </div>

          <h3>Inline code</h3>
          <p>Import the engine module: <code>github.com/stacklok/mecatl/engine</code> and wire a <code>port.LLMProvider</code>.</p>

          <h3>Admonitions</h3>
          <div className="admonition admonition-note alert alert--info">
            <div className="admonition-heading"><h5>note</h5></div>
            <div className="admonition-content"><p>The engine module has its own Go module path with a stable API contract.</p></div>
          </div>
          <div className="admonition admonition-warning alert alert--warning">
            <div className="admonition-heading"><h5>warning</h5></div>
            <div className="admonition-content"><p><code>engine/adapter/*</code> reference adapters carry no stability promise.</p></div>
          </div>
        </section>

        <section className={styles.section}>
          <h2>Design decisions</h2>
          <dl className={styles.decisions}>
            <dt>Dark-only</dt>
            <dd>The mascot's dark charcoal body makes the dark palette the canonical one. A light mode would require a different mascot treatment. Simplify: one mode, done well.</dd>
            <dt>Space Grotesk over a more "technical" face</dt>
            <dd>Space Grotesk is geometric and confident without being cold. It reads as a developer tool without feeling generic. Alternatives considered: Syne (too editorial), IBM Plex Sans (too IBM), Geist (too Vercel).</dd>
            <dt>Copper as secondary accent</dt>
            <dd>Copper (ear / tail color) provides contrast against teal and warmth against the charcoal background. Used sparingly for labels and eyebrows where a second color is needed.</dd>
            <dt>Landing page at /</dt>
            <dd>Docs at <code>/docs</code>, product page at <code>/</code>. Consumer-facing open-source projects need a pitch before the reference. The outline/intro doc is the first sidebar item, not the root URL.</dd>
          </dl>
        </section>

      </main>
    </Layout>
  );
}
