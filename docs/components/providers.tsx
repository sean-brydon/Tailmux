'use client';
import { RootProvider } from 'fumadocs-ui/provider/next';
import Search from './search';
export function Providers({children}: {children: React.ReactNode}) {
  return <RootProvider theme={{defaultTheme:'dark', enableSystem:false}} search={{SearchDialog:Search}}>{children}</RootProvider>;
}
