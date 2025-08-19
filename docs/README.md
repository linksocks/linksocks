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
│   └── config.js          # VitePress configuration
├── guide/                 # User guide
├── api/                   # HTTP API documentation  
├── python/                # Python bindings documentation
├── go/                    # Go CLI and library documentation
├── index.md               # Homepage
└── package.json           # Dependencies
```

## Contributing

When adding new documentation:

1. Follow the existing structure and naming conventions
2. Update the sidebar navigation in `.vitepress/config.js`
3. Use clear headings and code examples
4. Test locally before submitting PRs

## Deployment

The documentation is automatically deployed to GitHub Pages when changes are pushed to the main branch.
