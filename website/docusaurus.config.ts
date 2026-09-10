import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

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
      '@docusaurus/plugin-client-redirects',
      {
        redirects: [{from: ['/docs/intro'], to: '/docs'}],
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
      title: 'Mecatl',
      logo: {
        alt: 'Mecatl — stylized rope knot mark',
        src: 'img/logo.svg',
      },
      style: 'dark',
      items: [
        {
          to: '/docs',
          position: 'left',
          label: 'Docs',
        },
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
          title: 'Get started',
          items: [
            {label: 'Install', to: '/docs/install'},
            {label: 'Use mecatui', to: '/docs/mecatui'},
            {label: 'Getting started', to: '/docs/building/getting-started/demo'},
          ],
        },
        {
          title: 'Build',
          items: [
            {label: 'Build on Mecatl', to: '/docs/building'},
            {label: 'Extension points', to: '/docs/building/extension-points'},
            {label: 'Deployment', to: '/docs/building/deployment'},
          ],
        },
        {
          title: 'More',
          items: [
            {label: 'GitHub', href: 'https://github.com/stacklok/mecatl'},
            {label: 'Discord', href: 'https://discord.gg/stacklok'},
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
      additionalLanguages: ['bash', 'go', 'yaml', 'toml', 'json'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
