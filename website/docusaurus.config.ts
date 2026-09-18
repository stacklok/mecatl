import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// Tracking is enabled only for Vercel production deployments. Local
// development and Vercel previews omit it to avoid polluting analytics data.
const isProductionDeploy = process.env.VERCEL_ENV === 'production';

const config: Config = {
  title: 'Mecatl',
  tagline: 'A cloud-native harness for agentic systems',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
  },

  url: 'https://mecatl.dev',
  baseUrl: '/',

  onBrokenLinks: 'throw',
  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  themes: [
    '@docusaurus/theme-mermaid',
    [
      require.resolve('@easyops-cn/docusaurus-search-local'),
      {
        hashed: true,
        docsRouteBasePath: '/docs',
        docsDir: '../user-docs',
        indexBlog: false,
        highlightSearchTermsOnTargetPage: true,
      },
    ],
  ],
  plugins: [
    [
      'vercel-analytics',
      {
        debug: false,
      },
    ],
    [
      '@signalwire/docusaurus-plugin-llms-txt',
      {
        depth: 2,
        content: {
          includeBlog: false,
          includePages: true,
          includeDocs: true,
          includeGeneratedIndex: false,
          enableLlmsFullTxt: false,
          enableMarkdownFiles: true,
          excludeRoutes: ['/search'],
        },
        includeOrder: [
          '/docs/mecatui/**',
          '/docs/building/**',
          '/docs/features/**',
          '/docs/reference/**',
        ],
        optionalLinks: [
          {
            title: 'Mecatl on GitHub',
            url: 'https://github.com/stacklok/mecatl',
            description: 'Source code for Mecatl.',
          },
          {
            title: 'Community Discord',
            url: 'https://discord.gg/stacklok',
          },
        ],
      },
    ],
  ],

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          // Content lives in user-docs/ at the repo root, not inside website/.
          path: '../user-docs',
          routeBasePath: '/docs',
          sidebarPath: './sidebars.ts',
          editUrl: 'https://github.com/stacklok/mecatl/edit/main/user-docs/',
          showLastUpdateTime: true,
          showLastUpdateAuthor: true,
        },
        blog: false,
        googleTagManager: isProductionDeploy
          ? {containerId: 'GTM-KCC7R6SS'}
          : undefined,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      defaultMode: 'dark',
      disableSwitch: true,
      respectPrefersColorScheme: false,
    },
    navbar: {
      title: 'Mecatl',
      logo: {
        alt: 'Mecatl — stylized rope knot mark',
        src: 'img/logo.svg',
      },
      style: 'dark',
      items: [
        {
          to: '/docs/mecatui/getting-started',
          position: 'left',
          label: 'Get started',
        },
        {
          to: '/docs/cloud-native-harness',
          position: 'left',
          label: 'Cloud-native harness',
        },
        {
          to: '/docs/operating',
          position: 'left',
          label: 'Operate',
        },
        {
          to: '/docs/building',
          position: 'left',
          label: 'Build',
        },
        {
          href: 'https://github.com/stacklok/mecatl',
          label: 'GitHub',
          position: 'right',
        },
        {
          href: 'https://discord.gg/stacklok',
          label: 'Discord',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Get started',
          items: [
            {label: 'Use it now', to: '/docs/mecatui/getting-started'},
            {label: 'Run on Kubernetes', to: '/docs/operating/mecak8s'},
            {label: 'What is a cloud-native harness?', to: '/docs/cloud-native-harness'},
          ],
        },
        {
          title: 'Documentation',
          items: [
            {label: 'Use mecatui', to: '/docs/mecatui'},
            {label: 'Deploy and operate', to: '/docs/operating'},
            {label: 'Build with Mecatl', to: '/docs/building'},
            {label: 'Capabilities', to: '/docs/features'},
            {label: 'Reference', to: '/docs/reference'},
          ],
        },
        {
          title: 'More',
          items: [
            {label: 'GitHub', href: 'https://github.com/stacklok/mecatl'},
            {label: 'Discord', href: 'https://discord.gg/stacklok'},
            {label: 'Colophon', to: '/colophon'},
            {label: 'Stacklok', href: 'https://stacklok.com'},
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Stacklok, Inc.`,
    },
    prism: {
      // Dark-only site — only need the dark theme.
      theme: prismThemes.dracula,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'go', 'yaml', 'toml', 'json', 'protobuf', 'ini'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
