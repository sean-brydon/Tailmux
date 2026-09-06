import { createMDX } from 'fumadocs-mdx/next';
const withMDX = createMDX();
export default withMDX({
  output: 'export',
  trailingSlash: true,
  basePath: process.env.DOCS_BASE_PATH || '',
  images: { unoptimized: true },
  env: { NEXT_PUBLIC_BASE_PATH: process.env.DOCS_BASE_PATH || '' },
  turbopack: { root: import.meta.dirname },
});
