/* eslint-disable @typescript-eslint/no-var-requires */
const pkg = require("./package.json");

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "standalone",
  reactStrictMode: false,
  pageExtensions: ["js", "jsx", "mdx", "ts", "tsx"],
  eslint: {
    ignoreDuringBuilds: true,
  },
  // Keep better-sqlite3 as a server-side external so its native .node
  // bindings are preserved and bundled correctly in the standalone output.
  serverExternalPackages: ["better-sqlite3"],
  transpilePackages: ["@mesahub/ui"],
  env: {
    NEXT_PUBLIC_STUDIO_VERSION: pkg.version,
  },
  async headers() {
    return [
      {
        // Content-hashed chunks — immutable, cache for 1 year
        source: '/_next/static/:path*',
        headers: [{ key: 'Cache-Control', value: 'public, max-age=31536000, immutable' }],
      },
      {
        // Public folder assets (favicon, icons, etc.)
        source: '/:file(favicon.ico|robots.txt|sitemap.xml|sitemap.xsl)',
        headers: [{ key: 'Cache-Control', value: 'public, max-age=86400, stale-while-revalidate=3600' }],
      },
    ]
  },
};

module.exports = { ...nextConfig, output: "standalone" };
