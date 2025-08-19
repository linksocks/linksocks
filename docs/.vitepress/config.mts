import { defineConfig } from 'vitepress'
import {
  pagefindPlugin,
  chineseSearchOptimize,
} from 'vitepress-plugin-pagefind';

export default defineConfig({
  title: 'LinkSocks',
  description: 'SOCKS5 over WebSocket proxy tool',
  cleanUrls: true,
  head: [['link', { rel: 'icon', href: '/favicon.ico' }]],
  vite: {
    plugins: [
      pagefindPlugin({
        customSearchQuery: chineseSearchOptimize,
        btnPlaceholder: '搜索',
        placeholder: '搜索文档',
        emptyText: '空空如也',
        heading: '共: {{searchResult}} 条结果',
        excludeSelector: ['img', 'a.header-anchor'],
      }),
    ],
  },
  themeConfig: {
    logo: '/logo.png',
    nav: [
      { text: 'Guide', link: '/guide/' },
      { text: 'GitHub', link: 'https://github.com/linksocks/linksocks' }
    ],

    sidebar: [
      {
        text: 'Getting Started',
        items: [
          { text: 'Introduction', link: '/guide/' },
          { text: 'How It Works', link: '/guide/principles' },
          { text: 'Quick Start', link: '/guide/quick-start' }
        ]
      },
      {
        text: 'Advanced Topics',
        items: [
          { text: 'Command-line Options', link: '/guide/cli-options' },
          { text: 'Authentication', link: '/guide/authentication' },
          { text: 'Load Balancing', link: '/guide/load-balancing' },
          { text: 'Fast Open', link: '/guide/fast-open' },
          { text: 'HTTP API', link: '/guide/http-api' }
        ]
      },
      {
        text: 'Python Library',
        items: [
          { text: 'Overview', link: '/python/' },
          { text: 'Server Class', link: '/python/server' },
          { text: 'Client Class', link: '/python/client' },
          { text: 'Utilities', link: '/python/utilities' },
        ]
      },
      {
        text: 'Go Library',
        items: [
          { text: 'Overview', link: '/go/' },
          { text: 'Library Usage', link: '/go/library' },
          { text: 'Examples', link: '/go/examples' },
        ]
      }
    ],

    socialLinks: [
      { icon: 'github', link: 'https://github.com/linksocks/linksocks' }
    ],

    footer: {
      message: 'Released under the MIT License.',
      copyright: 'Copyright © 2025 LinkSocks Contributors'
    },

    editLink: {
      pattern: 'https://github.com/linksocks/linksocks/edit/main/docs/:path',
      text: 'Edit this page on GitHub'
    },

    lastUpdated: {
      text: 'Updated at',
      formatOptions: {
        dateStyle: 'full',
        timeStyle: 'medium'
      }
    },
  },
})
