import type { Metadata } from 'next';
import Link from 'next/link';
import { ArrowUpRight, Github, Terminal } from 'lucide-react';
import { DocsLayout } from 'fumadocs-ui/layouts/docs';
import { ThemeSwitch } from 'fumadocs-ui/layouts/shared/slots/theme-switch';
import { source } from '@/lib/source';
import { Providers } from '@/components/providers';
import { Mark } from '@/components/brand';
import '@fontsource-variable/dm-sans';
import '@fontsource-variable/jetbrains-mono';
import './globals.css';
export const metadata: Metadata = {
 title: {default:'Tailmux — Documentation',template:'%s · Tailmux'},
 description: 'Connect to development machines across separate Tailscale accounts. Set up Tailmux, configure SSH, and keep remote Herdr sessions running.',
 icons: {icon: `${process.env.DOCS_BASE_PATH || ''}/icon.svg`},
};
export default function Layout({children}:{children:React.ReactNode}) {
 return <html lang="en" suppressHydrationWarning><body><Providers>
  <a className="skip-link" href="#nd-page">Skip to content</a>
  <header className="site-header"><Link href="/" className="wordmark"><Mark/>tailmux<span className="docs-pill">docs</span></Link><div className="header-links"><Link className="header-docs" href="/docs">Documentation</Link><Link href="/docs/commands">CLI reference</Link><a href="https://github.com/sean-brydon/Tailmux" target="_blank" rel="noreferrer" className="github-link" aria-label="GitHub repository"><Github size={17}/><span>GitHub</span><ArrowUpRight size={13}/></a><ThemeSwitch className="site-theme-toggle" /></div></header>
  <DocsLayout tree={{...source.pageTree, children:source.pageTree.children.map(node=>node.type==='page' && node.url==='/docs' ? {...node,name:'Overview'} : node)}} nav={{title:<span className="sidebar-title">THE FIELD GUIDE</span>,url:'/docs'}} sidebar={{collapsible:false,footer:<Link className="sidebar-footer" href="/docs/installation"><span className="footer-icon"><Terminal size={17}/></span><span><strong>Made for your terminal.</strong><small>Install Tailmux <ArrowUpRight size={11}/></small></span></Link>}} themeSwitch={{enabled:false}}>
   {children}
  </DocsLayout>
 </Providers></body></html>
}
