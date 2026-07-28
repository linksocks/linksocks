# LinkSocks Documentation

This directory contains the VitePress documentation for LinkSocks.

## Development

### Prerequisites

- Node.js 18 or later
- npm or yarn

### Setup

```bash
cd docs
npm install
```

### Development Server

```bash
npm run dev
```

This will start the VitePress development server at `http://localhost:5173`.

### Build

```bash
npm run build
```

The built documentation will be in the `dist` directory.

### Preview

```bash
npm run preview
```

Preview the built documentation locally.

## Structure

```
docs/
├── .vitepress/
│   ├── config.mts         # VitePress configuration (includes Pagefind search)
│   └── theme/             # Custom theme overrides
├── guide/                 # User guide
├── python/                # Python bindings documentation
├── go/                    # Go CLI and library documentation
├── zh/                    # Simplified Chinese translations
├── index.md               # Homepage
└── package.json           # Dependencies
```

## Search

Full-text search is powered by [Pagefind](https://pagefind.app/) via
`vitepress-plugin-pagefind`. Indexes are generated during `pnpm build` and
support both English and Chinese queries (with `chineseSearchOptimize` for CJK
segmentation). The search UI strings are localized per locale.

## Contributing

When adding new documentation:

1. Follow the existing structure and naming conventions
2. Update the sidebar navigation in `.vitepress/config.mts`
3. Use clear headings and code examples
4. Test locally before submitting PRs

## Deployment

The documentation is automatically deployed to GitHub Pages when changes are pushed to the main branch.
