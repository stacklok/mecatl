import React from 'react';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import styles from './index.module.css';

const features = [
  {
    icon: '⟳',
    title: 'Complete agent loop',
    body: 'Multi-turn reasoning, tool dispatch, compaction, permission gating, and event streaming — all built in. You wire adapters; the loop runs.',
  },
  {
    icon: '⬡',
    title: 'Port model',
    body: 'Hexagonal design. Swap LLM backends, persistence, distributed locks, or permission logic without touching the engine. Reference adapters ship for every port.',
  },
  {
    icon: '☁',
    title: 'Cloud-native by design',
    body: 'Disposable process, externalized state, durable event record. mecak8s pre-wires Redis and k8s lease so you get stateless pods from day one.',
  },
  {
    icon: '⚡',
    title: 'Single-shot CI runner',
    body: 'mecatequi turns one prompt into a patch and an exit code. Forge-agnostic — plug it into any GitHub Actions, GitLab CI, or Jenkins pipeline.',
  },
  {
    icon: '◈',
    title: 'Importable engine core',
    body: 'github.com/stacklok/mecatl/engine is its own Go module with a tiny dependency closure. Embed the loop directly, no server required.',
  },
  {
    icon: '⚙',
    title: 'Full observability',
    body: 'OpenTelemetry traces, LLM resilience decorator, slow-turn ring buffer, and an MCP-accessible perf server. Instrument once, run anywhere.',
  },
];

function Hero() {
  return (
    <section className={styles.hero}>
      <div className={styles.heroInner}>
        <div className={styles.heroText}>
          <div className={styles.eyebrow}>Open source · Cloud-native · Go</div>
          <h1 className={styles.heroTitle}>
            An agentic harness
            <br />
            <span className={styles.heroAccent}>built to be embedded.</span>
          </h1>
          <p className={styles.heroSubtitle}>
            mecatl is a production-grade harness for building agentic systems.
            It ships the agent loop, port model, permissions, hooks, and
            resilience so you can focus on the adapter that makes it yours.
          </p>
          <div className={styles.heroCtas}>
            <Link className={styles.ctaPrimary} to="/docs/intro">
              Read the docs
            </Link>
            <Link className={styles.ctaSecondary} to="https://github.com/stacklok/mecatl">
              View on GitHub
            </Link>
          </div>
        </div>
        <div className={styles.heroMascot}>
          <img
            src="/img/mecatito.png"
            alt="Mecatito, the mecatl mascot — a Xoloitzcuintli puppy wearing a teal rope collar with Mesoamerican markings"
            className={styles.mascotImg}
          />
        </div>
      </div>
    </section>
  );
}

function Terminal() {
  return (
    <section className={styles.terminalSection}>
      <div className={styles.terminalWrap}>
        <div className={styles.terminalBar}>
          <span className={styles.dot} style={{background: '#ff5f56'}} />
          <span className={styles.dot} style={{background: '#ffbd2e'}} />
          <span className={styles.dot} style={{background: '#27c93f'}} />
          <span className={styles.terminalTitle}>mecademo — offline / mockllm</span>
        </div>
        <pre className={styles.terminalBody}>{`$ go run ./cmd/mecademo

=== mecatl demo (offline / mockllm) ===

[001] turn=0 turn.start
[002] turn=0 message.delta  text="I'll read the greeting file first."
[003] turn=0 tool.call      tool=Read  args={"path":"greeting.txt"}
[004] turn=0 tool.result    error=false result="hello from the mecatl demo workspace"
[005] turn=1 turn.start
[006] turn=1 message.delta  text="Now I'll save a short note."
[007] turn=1 permission.ask ASK tool=Write  -> client auto-approves
[008] turn=1 tool.call      tool=Write args={"path":"note.txt","content":"reviewed"}
[009] turn=1 tool.result    error=false result="wrote note.txt (8 bytes)"
[010] turn=2 turn.start
[011] turn=2 message.delta  text="Done: I read greeting.txt and saved note.txt."
[012] turn=0 result         stop=end_turn  usage: in=4100 out=125 cacheHitRate=0.88`}</pre>
      </div>
    </section>
  );
}

function Features() {
  return (
    <section className={styles.features}>
      <div className={styles.sectionInner}>
        <h2 className={styles.sectionTitle}>What you don't have to build</h2>
        <p className={styles.sectionSubtitle}>
          mecatl gives you the hard parts. Bring an LLM key and an idea.
        </p>
        <div className={styles.featureGrid}>
          {features.map((f) => (
            <div key={f.title} className={styles.featureCard}>
              <div className={styles.featureIcon}>{f.icon}</div>
              <h3 className={styles.featureTitle}>{f.title}</h3>
              <p className={styles.featureBody}>{f.body}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

function DeploymentShapes() {
  const shapes = [
    {label: 'Embed', desc: 'Import engine/ directly into your Go binary', link: '/docs/deployment/embed-engine'},
    {label: 'Serve', desc: 'Run mecated and drive it over gRPC or HTTP-SSE', link: '/docs/deployment/mecated'},
    {label: 'Cloud-native', desc: 'mecak8s: stateless pods, Redis, k8s lease', link: '/docs/deployment/mecak8s'},
    {label: 'CI', desc: 'mecatequi: one prompt → patch + exit code', link: '/docs/deployment/mecatequi'},
  ];

  return (
    <section className={styles.deploySection}>
      <div className={styles.sectionInner}>
        <h2 className={styles.sectionTitle}>Four deployment shapes</h2>
        <p className={styles.sectionSubtitle}>From embedded library to cloud-native service.</p>
        <div className={styles.deployGrid}>
          {shapes.map((s) => (
            <Link key={s.label} className={styles.deployCard} to={s.link}>
              <span className={styles.deployLabel}>{s.label}</span>
              <span className={styles.deployDesc}>{s.desc}</span>
            </Link>
          ))}
        </div>
      </div>
    </section>
  );
}

function GetStarted() {
  return (
    <section className={styles.getStarted}>
      <div className={styles.sectionInner}>
        <h2 className={styles.sectionTitle}>See it run in 60 seconds</h2>
        <p className={styles.sectionSubtitle}>
          No API key. No server. A real engine turn, offline.
        </p>
        <pre className={styles.installBlock}>{`go run github.com/stacklok/mecatl/cmd/mecademo@latest`}</pre>
        <div className={styles.getStartedLinks}>
          <Link className={styles.ctaPrimary} to="/docs/getting-started/demo">
            Full walkthrough →
          </Link>
          <Link className={styles.ctaSecondary} to="/docs/intro">
            Browse all docs
          </Link>
        </div>
      </div>
    </section>
  );
}

export default function Home(): React.ReactElement {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout title={siteConfig.title} description={siteConfig.tagline}>
      <main>
        <Hero />
        <Terminal />
        <Features />
        <DeploymentShapes />
        <GetStarted />
      </main>
    </Layout>
  );
}
