import React, {useState} from 'react';
import Head from '@docusaurus/Head';
import Link from '@docusaurus/Link';
import styles from './index.module.css';
import {
  useCenteredIndex,
  useCopy,
  useParallax,
  useRevealRoot,
  useScrolled,
  useStickyInstall,
} from '../components/home/hooks';

/* ─── Constants ────────────────────────────────────────────────────────────── */

const INSTALL_CMD = 'brew install stacklok/tap/mecatl';
const VERSION = 'v0.0.33';
const RELEASES = 'https://github.com/stacklok/mecatl/releases/latest';

const outcomes = [
  {title: 'Get your harness off the desktop', body: 'Start locally, then deploy a fleet of agents on Kubernetes with your choice of cloud and model provider.'},
  {title: 'Separate the agent loop from the sandbox', body: 'Separate control from execution to keep bad code outside your trust boundary.'},
  {title: 'Pick and choose what you need', body: 'Use Mecatl’s library of components to assemble a custom harness.'},
  {title: 'Control what your agents can do', body: 'Set permissions and choose which tool calls require your approval.'},
];

const capabilities: {title: string; body: React.ReactNode}[] = [
  {title: 'Complete agent loop', body: 'Multi-turn reasoning, tool dispatch, compaction, permission gating, and event streaming are built in.'},
  {title: 'Port model', body: 'Modular design. Swap LLM backends, persistence, distributed locks, or permission logic without touching the engine. Reference adapters included for every port.'},
  {title: 'Cloud-native by design', body: <>Disposable process, externalized state, durable event record. <code>mecak8s</code> pre-wires Redis and Kubernetes leases so you can scale stateless pods from day one.</>},
  {title: 'Importable engine', body: <><code>github.com/stacklok/mecatl/engine</code> is its own Go module with a tiny dependency closure. Embed the loop directly, no server required.</>},
  {title: 'Full observability', body: 'OpenTelemetry traces, LLM resilience decorator, slow-turn ring buffer, and an MCP-accessible perf server.'},
  {title: 'Identity at the core', body: 'Every agent acts under a verifiable identity. Mecatl stamps identity at every agent action, not just at the login gate.'},
];

const deployments: {word: string; desc: React.ReactNode; to: string}[] = [
  {word: 'Local or remote', desc: <>Run locally with <code>mecatui</code>, or connect to <code>mecated</code> over gRPC or HTTP/SSE.</>, to: '/docs/mecatui'},
  {word: 'Cloud-native', desc: <><code>mecak8s</code>: stateless pods, Redis-backed state, and multi-replica coordination.</>, to: '/docs/building/deployment/mecak8s'},
  {word: 'CI', desc: <>Use <code>mecatequi</code> to turn one prompt into a patch and a pass/fail result for your pipeline.</>, to: '/docs/building/deployment/mecatequi'},
  {word: 'Embed', desc: 'Import the engine directly into your Go project.', to: '/docs/building/deployment/embed-engine'},
];

const navLinks = [
  {label: 'Docs', to: '/docs'},
  {label: 'mecatui', to: '/docs/mecatui'},
  {label: 'Building', to: '/docs/building'},
  {label: 'GitHub', to: 'https://github.com/stacklok/mecatl'},
  {label: 'Discord', to: 'https://discord.gg/stacklok'},
];

const pad = (n: number) => String(n).padStart(2, '0');
const delay = (ms: number) => ({'--d': `${ms}ms`}) as React.CSSProperties;

/* ─── Nav ──────────────────────────────────────────────────────────────────── */

function Nav() {
  const scrolled = useScrolled(8);
  const [open, setOpen] = useState(false);
  const close = () => setOpen(false);
  const cls = [styles.nav, scrolled || open ? styles.navScrolled : '', open ? styles.navOpen : ''].join(' ');
  return (
    <header
      className={cls}
      onKeyDown={(e) => {
        if (e.key === 'Escape') close();
      }}
    >
      <Link to="/" className={styles.brand} aria-label="Mecatl home">
        <img src="/img/logo-line.svg" alt="" width={26} height={26} />
        Mecatl
      </Link>
      <nav id="primary-nav" className={styles.navLinks} aria-label="Primary">
        {navLinks.map((l) => (
          <Link key={l.to} to={l.to} onClick={close}>
            {l.label}
          </Link>
        ))}
        <a href="#install" className={styles.navInstall} onClick={close}>
          Install
        </a>
      </nav>
      <button
        type="button"
        className={styles.menuBtn}
        aria-expanded={open}
        aria-controls="primary-nav"
        onClick={() => setOpen((v) => !v)}
      >
        {open ? 'Close' : 'Menu'}
      </button>
    </header>
  );
}

/* ─── The install command, set as typography ───────────────────────────────── */

function InstallBar({id, revealFrom = 0}: {id?: string; revealFrom?: number}) {
  const [copied, copy] = useCopy(INSTALL_CMD);
  return (
    <>
      <div
        id={id}
        className={styles.cmd}
        data-reveal=""
        data-install-anchor={id ? 'hero' : 'closing'}
        style={delay(revealFrom)}
      >
        <code className={styles.cmdText}>{INSTALL_CMD}</code>
        <button
          type="button"
          className={`${styles.copy} ${copied ? styles.copied : ''}`}
          onClick={copy}
          aria-label={copied ? 'Copied install command' : 'Copy install command to clipboard'}
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <p className={styles.after} data-reveal="" style={delay(revealFrom + 100)}>
        <span>
          then: <code>mecatui --mock</code> <span className={styles.afterNote}>offline, no API key</span>
        </span>
        <Link to={RELEASES}>Download {VERSION}</Link>
        <Link to="/docs/install">Install guide</Link>
        {id ? null : <Link to="/docs">Explore the docs</Link>}
      </p>
    </>
  );
}

/* ─── 01 Hero ──────────────────────────────────────────────────────────────── */

function Hero() {
  // Desktop: four block lines. Phones: the lines become inline and the mobile-only
  // <br>s re-break the same words into six lines (see .h1 .line / .brM in the CSS).
  const br = <br className={styles.brM} />;
  const lines: React.ReactNode[] = [
    <>
      The open
      {br}
    </>,
    <>
      <em>cloud-native</em>
      {br} harness
    </>,
    <>
      {' for'}
      {br} developers who
      {br} are
    </>,
    <>
      {' building'}
      {br} platforms
    </>,
  ];
  const speeds = [-0.16, -0.11, -0.06, -0.02];
  return (
    <section className={`${styles.section} ${styles.hero}`} data-plx-section="">
      {/* ≥1024px the wrap is a grid: eyebrow and h1 span both columns, the copy and
          Mecatito share the last row (see .heroWrap); below that Mecatito is a cameo. */}
      <div className={`${styles.wrap} ${styles.heroWrap}`}>
        <p className={styles.eyebrow} data-reveal="">
          <b>{VERSION}</b> · Apache-2.0 · Open source
        </p>
        <h1 className={styles.h1}>
          {lines.map((l, i) => (
            <span key={i} className={styles.line} data-plx={speeds[i]}>
              <span className={styles.line} data-reveal="" style={delay(80 + i * 90)}>
                {l}
              </span>
            </span>
          ))}
        </h1>
        <div className={styles.heroBody}>
          <p className={styles.sub} data-reveal="" style={delay(480)}>
            We blew up the harness and then put it back together: more secure, more scalable, more extensible.
          </p>
          <InstallBar id="install" revealFrom={600} />
        </div>
        <img
          className={styles.mascot}
          src="/img/mecatito-cutout.png"
          alt="Mecatito, the Mecatl mascot: a Xoloitzcuintli puppy wearing a teal rope collar"
          width={920}
          height={1176}
          data-plx="0.14"
        />
      </div>
    </section>
  );
}

/* ─── 02 Statement ─────────────────────────────────────────────────────────── */

function Statement() {
  // Leading spaces are collapsed while the lines are blocks (desktop) and keep the
  // words apart when the lines flow as one paragraph (phones).
  const lines: React.ReactNode[] = [
    'Built from the ground up',
    ' to run in the cloud.',
    <>
      {' '}
      <em>Get your harness off your laptop</em>
    </>,
    ' and run fleets of agents anywhere.',
  ];
  return (
    <section className={`${styles.section} ${styles.alt}`} data-plx-section="">
      <div className={styles.wrap}>
        <p className={styles.statementText} data-plx="0.06">
          {lines.map((l, i) => (
            <span key={i} className={styles.line} data-reveal="" style={delay(i * 110)}>
              {l}
            </span>
          ))}
        </p>
      </div>
    </section>
  );
}

/* ─── 03 Outcomes ──────────────────────────────────────────────────────────── */

function Outcomes() {
  const [listRef, focused] = useCenteredIndex<HTMLOListElement>();
  return (
    <section className={styles.section} data-plx-section="">
      <div className={styles.wrap}>
        <header className={styles.head} data-plx-section="">
          <div data-plx="0.07">
            <p className={styles.label} data-reveal="">
              <b>01</b> Outcomes
            </p>
            <h2 className={styles.title} data-reveal="" style={delay(80)}>
              What can you do with Mecatl?
            </h2>
          </div>
        </header>
        <ol ref={listRef} className={styles.olist}>
          {outcomes.map((o, i) => (
            <li
              key={o.title}
              className={`${styles.oitem} ${focused >= 0 && focused !== i ? styles.oDim : ''}`}
              data-index={i}
            >
              <span className={styles.itemNum} aria-hidden="true">
                {pad(i + 1)}
              </span>
              <div data-reveal="">
                <h3 className={styles.itemTitle}>{o.title}</h3>
                <p className={styles.itemBody}>{o.body}</p>
              </div>
            </li>
          ))}
        </ol>
      </div>
    </section>
  );
}

/* ─── 04 Pronunciation ─────────────────────────────────────────────────────── */

function Pronunciation() {
  // A filled ghost of the phonetic drifts behind the sentence; Mecatito peeks over
  // the band's bottom edge (mirrored so he faces the words, see .pup) and says it.
  // The hero image carries the descriptive alt — this instance is decorative.
  return (
    <section className={`${styles.section} ${styles.alt} ${styles.pron}`} data-plx-section="">
      <span className={`${styles.ghost} ${styles.pronGhost}`} data-plx="0.25" aria-hidden="true">
        MEH-kah-tl
      </span>
      <div className={`${styles.wrap} ${styles.pronWrap}`}>
        <p className={styles.pronText} data-reveal="">
          Mecatl (<code>MEH-kah-tl</code>) is hard to say, but easy to use.
        </p>
      </div>
      {/* .pup is the (static) reveal target; the hop/bob run on .pupMotion inside it so the
          hidden pup, tucked below the band's clipped edge, can still be observed */}
      <div className={styles.pup} data-reveal="" aria-hidden="true">
        <div className={styles.pupMotion}>
          <div className={styles.pupFlip}>
            <img src="/img/mecatito-cutout.png" alt="" width={920} height={1176} loading="lazy" decoding="async" />
          </div>
          <span className={styles.bubble} data-bubble="">
            MEH-kah-tl
          </span>
        </div>
      </div>
    </section>
  );
}

/* ─── 05 Capabilities ──────────────────────────────────────────────────────── */

function Capabilities() {
  // Two columns of title + body pairs on desktop (one below 900px); the
  // right-hand item of each row reveals a beat after the left.
  return (
    <section className={styles.section} data-plx-section="">
      <div className={styles.wrap}>
        <header className={styles.head} data-plx-section="">
          <div data-plx="0.07">
            <p className={styles.label} data-reveal="">
              <b>02</b> Capabilities
            </p>
            <h2 className={styles.title} data-reveal="" style={delay(80)}>
              What you don&apos;t have to build
            </h2>
            <p className={styles.lede} data-reveal="" style={delay(160)}>
              Mecatl gives you the hard parts. Use what you need.
            </p>
          </div>
        </header>
        <ol className={styles.capList}>
          {capabilities.map((c, i) => (
            <li key={c.title} className={styles.capItem}>
              <span className={styles.itemNum} aria-hidden="true">
                {pad(i + 1)}
              </span>
              <div data-reveal="" style={delay((i % 2) * 90)}>
                <h3 className={styles.itemTitle}>{c.title}</h3>
                <p className={styles.itemBody}>{c.body}</p>
              </div>
            </li>
          ))}
        </ol>
      </div>
    </section>
  );
}

/* ─── 06 Deployment ────────────────────────────────────────────────────────── */

function Deployment() {
  return (
    <section className={`${styles.section} ${styles.alt}`} data-plx-section="">
      <div className={styles.wrap}>
        <header className={styles.head} data-plx-section="">
          <div data-plx="0.07">
            <p className={styles.label} data-reveal="">
              <b>03</b> Deployment
            </p>
            <h2 className={styles.title} data-reveal="" style={delay(80)}>
              Four deployment options to get you started
            </h2>
          </div>
        </header>
        <ul className={styles.deployList}>
          {deployments.map((d, i) => (
            <li key={d.word} className={styles.deployItem} data-reveal="" style={delay(i * 70)}>
              {/* an index row: word · description ……… arrow (see .deployLink grid areas) */}
              <Link to={d.to} className={styles.deployLink}>
                <span className={styles.deployWord}>{d.word}</span>
                <span className={styles.deployDesc}>{d.desc}</span>
                <span className={styles.deployArrow} aria-hidden="true">
                  →
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}

/* ─── 07 Stacklok ──────────────────────────────────────────────────────────── */

function Stacklok() {
  return (
    <section className={styles.section} data-plx-section="">
      <div className={styles.wrap}>
        <header className={styles.head} data-plx-section="">
          <div data-plx="0.07">
            <p className={styles.label} data-reveal="">
              <b>04</b> Stacklok
            </p>
            <h2 className={`${styles.title} ${styles.quote}`} data-reveal="" style={delay(80)}>
              Mecatl is part of Stacklok&apos;s commitment to building a more open alternative to vertically integrated agent stacks.
            </h2>
          </div>
        </header>
        <div className={styles.featured} data-reveal="">
          <p className={styles.label}>Featured project</p>
          <div className={styles.featuredRow}>
            <h3 className={styles.projectName}>
              <img src="/img/toolhive-logo.svg" alt="" width={32} height={32} />
              ToolHive
            </h3>
            <div>
              <p className={styles.projectBody}>The open source MCP platform trusted by enterprises, securing 10M tool calls each month.</p>
              <Link to="https://github.com/stacklok/toolhive" className={styles.projectLink}>
                ToolHive on GitHub
              </Link>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

/* ─── 08 Closing ───────────────────────────────────────────────────────────── */

function Closing() {
  return (
    <section className={`${styles.section} ${styles.alt}`} data-plx-section="">
      <span className={`${styles.ghost} ${styles.closingGhost}`} data-plx="0.32" aria-hidden="true">
        open
      </span>
      <div className={styles.wrap}>
        <header className={styles.head} data-plx-section="">
          <div data-plx="0.07">
            <h2 className={styles.closingTitle} data-reveal="">
              One line to install.
            </h2>
          </div>
        </header>
        <InstallBar revealFrom={120} />
      </div>
    </section>
  );
}

/* ─── Footer ───────────────────────────────────────────────────────────────── */

function Footer() {
  return (
    <footer className={styles.footer}>
      <div className={`${styles.wrap} ${styles.footGrid}`}>
        <div className={styles.footBrand}>
          <Link to="/" className={styles.brand} aria-label="Mecatl home">
            <img src="/img/logo-line.svg" alt="" width={26} height={26} />
            Mecatl
          </Link>
          <p>The open cloud-native harness. Built by Stacklok.</p>
        </div>
        <div className={styles.footCol}>
          <h3>Get started</h3>
          <Link to="/docs/install">Install</Link>
          <Link to="/docs/mecatui">Use mecatui</Link>
          <Link to="/docs/building/getting-started/demo">Getting started</Link>
        </div>
        <div className={styles.footCol}>
          <h3>Build</h3>
          <Link to="/docs/building">Build on Mecatl</Link>
          <Link to="/docs/building/extension-points">Extension points</Link>
          <Link to="/docs/building/deployment">Deployment</Link>
        </div>
        <div className={styles.footCol}>
          <h3>More</h3>
          <Link to="https://github.com/stacklok/mecatl">GitHub</Link>
          <Link to="https://discord.gg/stacklok">Discord</Link>
          <Link to="https://stacklok.com">Stacklok</Link>
        </div>
      </div>
      <div className={`${styles.wrap} ${styles.footBottom}`}>
        <span>Copyright © 2026 Stacklok, Inc.</span>
        <span>
          {VERSION} · Apache-2.0
        </span>
      </div>
    </footer>
  );
}

/* ─── Sticky install bar (phones) ──────────────────────────────────────────── */

function StickyInstall() {
  const show = useStickyInstall();
  const [copied, copy] = useCopy(INSTALL_CMD);
  return (
    <div className={`${styles.stickyBar} ${show ? styles.stickyBarOn : ''}`}>
      <code className={styles.stickyCmd}>{INSTALL_CMD}</code>
      <button
        type="button"
        className={`${styles.stickyCopy} ${copied ? styles.copied : ''}`}
        onClick={copy}
        aria-label={copied ? 'Copied install command' : 'Copy install command to clipboard'}
      >
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  );
}

/* ─── Page ─────────────────────────────────────────────────────────────────── */

export default function Home(): React.ReactElement {
  const revealRef = useRevealRoot<HTMLDivElement>();
  const plxRef = useParallax<HTMLElement>();
  return (
    <div ref={revealRef} className={styles.root}>
      <Head>
        <title>Mecatl — The open cloud-native harness</title>
        <meta name="description" content="The open cloud-native harness for developers who are building platforms." />
        <link rel="preconnect" href="https://fonts.googleapis.com" />
        <link rel="preconnect" href="https://fonts.gstatic.com" crossOrigin="anonymous" />
        <link href="https://fonts.googleapis.com/css2?family=Instrument+Sans:wght@600;700&display=swap" rel="stylesheet" />
      </Head>
      <Nav />
      <main ref={plxRef}>
        <Hero />
        <Statement />
        <Outcomes />
        <Pronunciation />
        <Capabilities />
        <Deployment />
        <Stacklok />
        <Closing />
      </main>
      <Footer />
      <StickyInstall />
    </div>
  );
}
