import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'mecatl',
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
      onBrokenMarkdownLinks: 'warn',
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
      title: 'mecatl',
      logo: {
        alt: 'mecatl — stylized rope knot mark',
        src: 'img/logo.svg',
      },
      style: 'dark',
      items: [
        {
          to: '/docs/mecatui',
          position: 'left',
          label: 'mecatui',
        },
        {
          to: '/docs/building',
          position: 'left',
          label: 'Building',
        },
        {
          to: '/colophon',
          label: 'Colophon',
          position: 'right',
        },
        {
          href: 'https://github.com/stacklok/mecatl',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Guides',
          items: [
            {label: 'Use mecatui', to: '/docs/mecatui'},
            {label: 'Build on mecatl', to: '/docs/building'},
            {label: 'Getting Started', to: '/docs/building/getting-started/demo'},
            {label: 'Extension Points', to: '/docs/building/extension-points'},
            {label: 'Deployment', to: '/docs/building/deployment'},
          ],
        },
        {
          title: 'More',
          items: [
            {label: 'GitHub', href: 'https://github.com/stacklok/mecatl'},
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Stacklok, Inc. Built with Docusaurus.`,
    },
    prism: {
      // Dark-only site — only need the dark theme.
      theme: prismThemes.dracula,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'go', 'yaml', 'toml', 'json'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
