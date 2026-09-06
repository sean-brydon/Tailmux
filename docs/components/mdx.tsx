import defaultMdxComponents from 'fumadocs-ui/mdx';
import { Cards, Card } from 'fumadocs-ui/components/card';
import { Callout } from 'fumadocs-ui/components/callout';
import { Steps, Step } from 'fumadocs-ui/components/steps';
import { ConnectionMap } from './connection-map';
import { Terminal } from './terminal';
import type { MDXComponents } from 'mdx/types';
export function getMDXComponents(components?: MDXComponents) {
 return {...defaultMdxComponents, Cards, Card, Callout, Steps, Step, ConnectionMap, Terminal, ...components} satisfies MDXComponents;
}
export const useMDXComponents=getMDXComponents;
