import { docs } from '../.source/server';
import { loader } from 'fumadocs-core/source';
import { createElement } from 'react';
import { BookOpen, Download, Terminal, Network, Settings2, KeyRound, Activity, Layers, Route, GitBranch } from 'lucide-react';
const icons = { BookOpen, Download, Terminal, Network, Settings2, KeyRound, Activity, Layers, Route, GitBranch };
export const source = loader({
  baseUrl: '/docs',
  source: docs.toFumadocsSource(),
  icon(name) { return name && name in icons ? createElement(icons[name as keyof typeof icons]) : undefined; },
});
