import React from 'react';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import styles from './index.module.css';
import {
  useParallax,
  useRevealRoot,
} from '../components/home/hooks';

const outcomes = [
  {icon: '☁', title: 'Get your harness off the desktop', body: 'Start locally, then deploy a fleet of agents on Kubernetes with your choice of cloud and model provider.'},
  {icon: '⌘', title: 'Separate the agent loop from the sandbox', body: 'Separate control from execution to keep bad code outside your trust boundary.'},
  {icon: '⬡', title: 'Pick and choose what you need', body: 'Use Mecatl\'s library of components to assemble a custom harness.'},
  {icon: '✓', title: 'Control what your agents can do', body: 'Set permissions and choose which tool calls require your approval.'},
];

const capabilities: {icon: string; title: string; body: React.ReactNode}[] = [
  {icon: '⟳', title: 'Complete agent loop', body: 'Multi-turn reasoning, tool dispatch, compaction, permission gating, and event streaming are built in.'},
  {icon: '⬡', title: 'Port model', body: 'Modular design. Swap LLM backends, persistence, distributed locks, or permission logic without touching the engine. Reference adapters included for every port.'},
  {icon: '☁', title: 'Cloud-native by design', body: <>Disposable process, externalized state, durable event record. <code>mecak8s</code> pre-wires Redis and Kubernetes leases so you can scale stateless pods from day one.</>},
  {icon: '◈', title: 'Importable engine', body: <><code>github.com/stacklok/mecatl/engine</code> is its own Go module with a tiny dependency closure. Embed the loop directly, no server required.</>},
  {icon: '⚙', title: 'Full observability', body: 'OpenTelemetry traces, LLM resilience decorator, slow-turn ring buffer, and an MCP-accessible perf server.'},
  {icon: '✓', title: 'Identity at the core', body: 'Every agent acts under a verifiable identity. Mecatl stamps identity at every agent action, not just at the login gate.'},
];

const deployments: {label: string; desc: React.ReactNode; link: string}[] = [
  {label: 'Local or remote', desc: <>Run locally with <code>mecatui</code>, or connect to <code>mecated</code> over gRPC or HTTP/SSE.</>, link: '/docs/mecatui'},
  {label: 'Cloud-native', desc: <><code>mecak8s</code>: stateless pods, Redis-backed state, and multi-replica coordination.</>, link: '/docs/building/deployment/mecak8s'},
  {label: 'CI', desc: <>Use <code>mecatequi</code> to turn one prompt into a patch and a pass/fail result for your pipeline.</>, link: '/docs/building/deployment/mecatequi'},
  {label: 'Embed', desc: 'Import the engine directly into your Go project.', link: '/docs/building/deployment/embed-engine'},
];

const delay = (ms: number) => ({'--d': `${ms}ms`}) as React.CSSProperties;

function Hero() {
  return (
    <section className={`${styles.section} ${styles.hero}`} data-plx-section="">
      <div className={`${styles.wrap} ${styles.heroWrap}`}>
        <div className={styles.heroBody}>
          <h1 className={styles.h1} data-reveal="">
            The open source, <Link className={styles.heroTitleLink} to="/docs/building/cloud-native-harness">cloud-native harness</Link>
          </h1>
          <p className={styles.sub} data-reveal="" style={delay(100)}>
            Build and run capable agents with the same platform patterns you use for the rest of your software.
          </p>
          <div className={styles.heroCtas} data-reveal="" style={delay(200)}>
            <Link className={styles.ctaPrimary} to="/docs/mecatui/getting-started">Use it now</Link>
            <Link className={styles.ctaSecondary} to="/docs/building/cloud-native-harness">What is a cloud-native harness?</Link>
            <Link className={styles.ctaSecondary} to="/docs/building/deployment/mecak8s">Run on Kubernetes</Link>
          </div>
        </div>
        <Link className={styles.mascot} to="/colophon" data-plx="0.08">
          <img
            src="/img/mecatito-cutout.png"
            alt="Mecatito, the Mecatl mascot, a Xoloitzcuintli puppy wearing a teal rope collar with Mesoamerican markings"
            width={920}
            height={1176}
          />
        </Link>
      </div>
    </section>
  );
}

function CloudNativeStatement() {
  return (
    <section className={`${styles.section} ${styles.alt}`} data-plx-section="">
      <div className={styles.wrap}>
        <p className={styles.statementText} data-reveal="">
          Built from the ground up to run in the cloud. Get your harness off your laptop and run fleets of agents anywhere.
        </p>
      </div>
    </section>
  );
}

function Outcomes() {
  return (
    <section className={styles.section} data-plx-section="">
      <div className={styles.wrap}>
        <header className={styles.head}>
          <h2 className={styles.title} data-reveal="">What can you do with Mecatl?</h2>
        </header>
        <ul className={styles.olist}>
          {outcomes.map((outcome) => (
            <li key={outcome.title} className={styles.oitem}>
              <span className={styles.itemIcon} aria-hidden="true">{outcome.icon}</span>
              <div data-reveal="">
                <h3 className={styles.itemTitle}>{outcome.title}</h3>
                <p className={styles.itemBody}>{outcome.body}</p>
              </div>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}

function Pronunciation() {
  return (
    <section className={`${styles.section} ${styles.alt} ${styles.pron}`} data-plx-section="">
      <span className={`${styles.ghost} ${styles.pronGhost}`} data-plx="0.18" aria-hidden="true">MEH-kah-tl</span>
      <div className={`${styles.wrap} ${styles.pronWrap}`}>
        <h2 className={styles.pronText} data-reveal="">Mecatl (MEH-kah-tl) is hard to say, but easy to use.</h2>
      </div>
      <div className={styles.pup} data-reveal="" aria-hidden="true">
        <div className={styles.pupMotion}>
          <div className={styles.pupFlip}>
            <img src="/img/mecatito-cutout.png" alt="" width={920} height={1176} loading="lazy" decoding="async" />
          </div>
        </div>
      </div>
    </section>
  );
}

function Capabilities() {
  return (
    <section className={styles.section} data-plx-section="">
      <div className={styles.wrap}>
        <header className={styles.head}>
          <h2 className={styles.title} data-reveal="">What you don&apos;t have to build</h2>
          <p className={styles.lede} data-reveal="" style={delay(80)}>Mecatl gives you the hard parts. Use what you need.</p>
        </header>
        <ul className={styles.capList}>
          {capabilities.map((capability, i) => (
            <li key={capability.title} className={styles.capItem}>
              <span className={styles.itemIcon} aria-hidden="true">{capability.icon}</span>
              <div data-reveal="" style={delay((i % 2) * 90)}>
                <h3 className={styles.itemTitle}>{capability.title}</h3>
                <p className={styles.itemBody}>{capability.body}</p>
              </div>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}

function DeploymentOptions() {
  return (
    <section className={`${styles.section} ${styles.alt}`} data-plx-section="">
      <div className={styles.wrap}>
        <header className={styles.head}>
          <h2 className={styles.title} data-reveal="">Four deployment options to get you started</h2>
        </header>
        <ul className={styles.deployList}>
          {deployments.map((deployment, i) => (
            <li key={deployment.label} className={styles.deployItem} data-reveal="" style={delay(i * 70)}>
              <Link
                to={deployment.link}
                className={styles.deployLink}
                target="_blank"
                rel="noopener noreferrer"
              >
                <span className={styles.deployWord}>{deployment.label}</span>
                <span className={styles.deployDesc}>{deployment.desc}</span>
              </Link>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}

function Stacklok() {
  return (
    <section className={styles.section} data-plx-section="">
      <div className={styles.wrap}>
        <header className={styles.head}>
          <h2 className={`${styles.title} ${styles.quote}`} data-reveal="">
            Mecatl is part of Stacklok&apos;s commitment to building a more open alternative to vertically integrated agent stacks.
          </h2>
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
              <Link className={styles.ctaPrimary} to="https://github.com/stacklok/toolhive">View on GitHub</Link>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}

export default function Home(): React.ReactElement {
  const {siteConfig} = useDocusaurusContext();
  const revealRef = useRevealRoot<HTMLDivElement>();
  const parallaxRef = useParallax<HTMLElement>();
  return (
    <Layout title={siteConfig.title} description={siteConfig.tagline}>
      <div ref={revealRef} className={styles.root}>
        <main ref={parallaxRef}>
          <Hero />
          <CloudNativeStatement />
          <Outcomes />
          <Pronunciation />
          <Capabilities />
          <DeploymentOptions />
          <Stacklok />
        </main>
      </div>
    </Layout>
  );
}
