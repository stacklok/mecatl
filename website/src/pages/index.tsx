import React from 'react';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import styles from './index.module.css';

type Card = {
  icon: string;
  title: string;
  body: React.ReactNode;
};

const outcomes: Card[] = [
  { icon: '☁', title: 'Get your harness off the desktop', body: 'Start locally, then deploy a fleet of agents on Kubernetes with your choice of cloud and model provider.' },
  { icon: '⌘', title: 'Separate the agent loop from the sandbox', body: 'Separate control from execution to keep bad code outside your trust boundary.' },
  { icon: '⬡', title: 'Pick and choose what you need', body: 'Use Mecatl\'s library of components to assemble a custom harness.' },
  { icon: '✓', title: 'Control what your agents can do', body: 'Set permissions and choose which tool calls require your approval.' },
];

const capabilities: Card[] = [
  { icon: '⟳', title: 'Complete agent loop', body: 'Multi-turn reasoning, tool dispatch, compaction, permission gating, and event streaming are built in.' },
  { icon: '⬡', title: 'Port model', body: 'Modular design. Swap LLM backends, persistence, distributed locks, or permission logic without touching the engine. Reference adapters included for every port.' },
  { icon: '☁', title: 'Cloud-native by design', body: <>Disposable process, externalized state, durable event record. <code>mecak8s</code> pre-wires Redis and Kubernetes leases so you can scale stateless pods from day one.</> },
  { icon: '◈', title: 'Importable engine', body: <><code>github.com/stacklok/mecatl/engine</code> is its own Go module with a tiny dependency closure. Embed the loop directly, no server required.</> },
  { icon: '⚙', title: 'Full observability', body: 'OpenTelemetry traces, LLM resilience decorator, slow-turn ring buffer, and an MCP-accessible perf server.' },
  { icon: '✓', title: 'Identity at the core', body: 'Every agent acts under a verifiable identity. Mecatl stamps identity at every agent action, not just at the login gate.' },
];

function CardGrid({cards, className = ''}: {cards: Card[]; className?: string}) {
  return <div className={`${styles.featureGrid} ${className}`}>{cards.map((card) => <article key={card.title} className={styles.featureCard}><div className={styles.featureIcon}>{card.icon}</div><h3 className={styles.featureTitle}>{card.title}</h3><p className={styles.featureBody}>{card.body}</p></article>)}</div>;
}

function Hero() {
  return <section className={styles.hero}><div className={styles.heroInner}><div className={styles.heroText}><h1 className={styles.heroTitle}>The open cloud-native harness for developers who are building platforms</h1><p className={styles.heroSubtitle}>We blew up the harness and then put it back together: more secure, more scalable, more extensible.</p><div className={styles.heroCtas}><Link className={styles.ctaPrimary} to="https://github.com/stacklok/mecatl">Start building with Mecatl</Link><Link className={styles.ctaSecondary} to="/docs/intro">Explore the docs</Link><Link className={styles.ctaSecondary} to="https://discord.gg/stacklok">Engage via Discord</Link></div></div><div className={styles.heroMascot}><img src="/img/mecatito.png" alt="Mecatito, the Mecatl mascot, a Xoloitzcuintli puppy wearing a teal rope collar with Mesoamerican markings" className={styles.mascotImg} /></div></div></section>;
}

function CloudNativeStatement() {
  return <section className={styles.statement}><div className={styles.sectionInner}><p>Built from the ground up to run in the cloud. Get your harness off your laptop and run fleets of agents anywhere.</p></div></section>;
}

function Outcomes() {
  return <section className={styles.outcomes}><div className={styles.sectionInner}><h2 className={styles.sectionTitle}>What can you do with Mecatl?</h2><CardGrid cards={outcomes} /></div></section>;
}

function Pronunciation() {
  return <section className={styles.pronunciation}><div className={styles.pronunciationInner}><img src="/img/mecatito.png" alt="Mecatito, the Mecatl mascot" /><h2>Mecatl (MEH-kah-tl) is hard to say, but easy to use.</h2></div></section>;
}

function Capabilities() {
  return <section className={styles.features}><div className={styles.sectionInner}><h2 className={styles.sectionTitle}>What you don&apos;t have to build</h2><p className={styles.sectionSubtitle}>Mecatl gives you the hard parts. Use what you need.</p><CardGrid cards={capabilities} className={styles.capabilityGrid} /></div></section>;
}

function DeploymentOptions() {
  const options = [
    {label: 'Local or remote', desc: <>Run locally with <code>mecatui</code>, or connect to <code>mecated</code> over gRPC or HTTP/SSE.</>, link: '/docs/mecatui'},
    {label: 'Cloud-native', desc: <><code>mecak8s</code>: stateless pods, Redis-backed state, and multi-replica coordination.</>, link: '/docs/building/deployment/mecak8s'},
    {label: 'CI', desc: <>Use <code>mecatequi</code> to turn one prompt into a patch and a pass/fail result for your pipeline.</>, link: '/docs/building/deployment/mecatequi'},
    {label: 'Embed', desc: 'Import the engine directly into your Go project.', link: '/docs/building/deployment/embed-engine'},
  ];
  return <section className={styles.deploySection}><div className={styles.sectionInner}><h2 className={styles.sectionTitle}>Four deployment options to get you started</h2><div className={styles.deployGrid}>{options.map((option) => <Link key={option.label} className={styles.deployCard} to={option.link}><span className={styles.deployLabel}>{option.label}</span><span className={styles.deployDesc}>{option.desc}</span></Link>)}</div></div></section>;
}

function Stacklok() {
  return <section className={styles.stacklok}><div className={styles.sectionInner}><h2 className={styles.sectionTitle}>Mecatl is part of Stacklok&apos;s commitment to building a more open alternative to vertically integrated agent stacks.</h2><div className={styles.projectCallout}><div><p className={styles.projectLabel}>Featured project</p><div className={styles.projectContent}><img className={styles.toolHiveLogo} src="/img/toolhive-logo.svg" alt="" /><div><h3>ToolHive</h3><p>The open source MCP platform trusted by enterprises, securing 10M tool calls each month.</p></div></div></div><Link className={styles.ctaPrimary} to="https://github.com/stacklok/toolhive">View on GitHub</Link></div></div></section>;
}

export default function Home(): React.ReactElement {
  const {siteConfig} = useDocusaurusContext();
  return <Layout title={siteConfig.title} description={siteConfig.tagline}><main><Hero /><CloudNativeStatement /><Outcomes /><Pronunciation /><Capabilities /><DeploymentOptions /><Stacklok /></main></Layout>;
}
