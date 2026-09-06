'use client';
import { useState } from 'react';
import { Check, Copy, Terminal as TerminalIcon } from 'lucide-react';
export function Terminal({code, title='Terminal'}: {code:string; title?:string}) {
  const [copied,setCopied]=useState(false);
  const [error,setError]=useState(false);
  async function copy() { try {await navigator.clipboard.writeText(code);setCopied(true);setError(false);setTimeout(()=>setCopied(false),1800);}catch{setError(true);} }
  return <div className="terminal not-prose"><div className="terminal-bar"><span><TerminalIcon size={14}/>{title}</span><button aria-label="Copy command" onClick={copy}>{copied?<Check size={15}/>:<Copy size={15}/>}<span>{copied?'Copied':'Copy'}</span></button></div><pre><code>{code.split('\n').map((line,i)=><span className="terminal-line" key={i}><span className={line.startsWith('#')?'comment':'prompt'}>{line.startsWith('#')?'':'$ '}</span><span className={line.startsWith('#')?'comment':''}>{line}</span>{'\n'}</span>)}</code></pre>{error&&<p role="status" className="copy-error">Select the command to copy it manually.</p>}</div>;
}
