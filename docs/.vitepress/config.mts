import { defineConfig } from 'vitepress'
import { chineseSearchOptimize, pagefindPlugin } from 'vitepress-plugin-pagefind'

export default defineConfig({
  title: 'LinkSocks',
  description: 'SOCKS5 over WebSocket proxy tool',
  cleanUrls: true,
  lastUpdated: true,
  head: [['link', { rel: 'icon', href: '/favicon.ico' }]],

  // Offline full-text search with Chinese segmentation support.
  // Replaces the built-in MiniSearch local provider, which struggles with CJK text.
  vite: {
    plugins: [
      pagefindPlugin({
        customSearchQuery: chineseSearchOptimize,
        excludeSelector: ['img', 'a.header-anchor', 'div.aside'],
        locales: {
          root: {
            btnPlaceholder: 'Search',
            placeholder: 'Search docs...',
            emptyText: 'No results found',
            heading: 'Total: {{searchResult}} search results.',
            toSelect: 'to select',
            toNavigate: 'to navigate',
            toClose: 'to close',
            searchBy: 'Search by'
          },
          zh: {
            btnPlaceholder: '搜索',
            placeholder: '搜索文档...',
            emptyText: '未找到相关结果',
            heading: '共 {{searchResult}} 条结果',
            toSelect: '选择',
            toNavigate: '切换',
            toClose: '关闭',
            searchBy: '搜索引擎'
          }
        }
      })
    ]
  },

  locales: {
    root: {
      label: 'English',
      lang: 'en',
      title: 'LinkSocks',
      description: 'SOCKS5 over WebSocket proxy tool',
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
              { text: 'Message Protocol', link: '/guide/message-protocol' },
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
        }
      }
    },
    zh: {
      label: '简体中文',
      lang: 'zh-CN',
      title: 'LinkSocks',
      description: '基于 WebSocket 的 SOCKS5 代理工具',
      themeConfig: {
        logo: '/logo.png',
        nav: [
          { text: '指南', link: '/zh/guide/' },
          { text: 'GitHub', link: 'https://github.com/linksocks/linksocks' }
        ],
        sidebar: [
          {
            text: '快速开始',
            items: [
              { text: '介绍', link: '/zh/guide/' },
              { text: '工作原理', link: '/zh/guide/principles' },
              { text: '快速入门', link: '/zh/guide/quick-start' }
            ]
          },
          {
            text: '进阶主题',
            items: [
              { text: '命令行选项', link: '/zh/guide/cli-options' },
              { text: '身份验证', link: '/zh/guide/authentication' },
              { text: '负载均衡', link: '/zh/guide/load-balancing' },
              { text: '快速打开', link: '/zh/guide/fast-open' },
              { text: '消息协议', link: '/zh/guide/message-protocol' },
              { text: 'HTTP API', link: '/zh/guide/http-api' }
            ]
          },
          {
            text: 'Python 库',
            items: [
              { text: '概述', link: '/zh/python/' },
              { text: 'Server 类', link: '/zh/python/server' },
              { text: 'Client 类', link: '/zh/python/client' },
              { text: '工具函数', link: '/zh/python/utilities' },
            ]
          },
          {
            text: 'Go 库',
            items: [
              { text: '概述', link: '/zh/go/' },
              { text: '库的使用', link: '/zh/go/library' },
              { text: '示例', link: '/zh/go/examples' },
            ]
          }
        ],
        socialLinks: [
          { icon: 'github', link: 'https://github.com/linksocks/linksocks' }
        ],
        footer: {
          message: '基于 MIT 许可证发布。',
          copyright: '版权所有 © 2025 LinkSocks 贡献者'
        },
        editLink: {
          pattern: 'https://github.com/linksocks/linksocks/edit/main/docs/:path',
          text: '在 GitHub 上编辑此页'
        },
        lastUpdated: {
          text: '最后更新于',
          formatOptions: {
            dateStyle: 'full',
            timeStyle: 'medium'
          }
        }
      }
    }
  }
})
