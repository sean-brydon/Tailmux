'use client';
import { useState } from 'react';
import { Laptop, Server, ArrowUpRight, Terminal, Check, Copy } from 'lucide-react';
const targets={personal:{host:'devbox',profile:'personal'},work:{host:'buildbox',profile:'work'}};
export function ConnectionMap(){
 const [active,setActive]=useState<'personal'|'work'>('personal');
 const [copied,setCopied]=useState(false);
 const target=targets[active];
 const command=`tailmux attach ${target.profile}/${target.host}`;
 return <div className="connection-map not-prose">
  <div className="map-heading"><span className="micro-label">TWO TAILNETS. ONE TERMINAL.</span><span className="example-label">Interactive example</span></div>
  <div className={`map-body active-${active}`}>
   <div className="local-node"><span className="node-icon"><Laptop size={23}/></span><strong>Your machine</strong><span>tailmux daemon</span><div className="profile-tags"><i/>tsnet × 2</div></div>
   <div className="map-wires" aria-hidden="true"><svg viewBox="0 0 160 170" preserveAspectRatio="none"><path className={active==='personal'?'wire selected':'wire'} d="M0 85H60Q80 85 80 65V38Q80 22 98 22H160"/><path className={active==='work'?'wire selected work':'wire work'} d="M0 85H60Q80 85 80 105V132Q80 148 98 148H160"/></svg><span>encrypted</span></div>
   <div className="remote-nodes">{Object.entries(targets).map(([id,t])=><button key={id} className={`remote-node ${active===id?'selected':''} ${id}`} onClick={()=>setActive(id as typeof active)} aria-pressed={active===id}><Server size={20}/><span><strong>{t.host}</strong><small>{t.profile} tailnet</small></span><span className="node-dot"/></button>)}</div>
  </div>
  <div className="map-command"><Terminal size={15}/><code><span>$</span> {command}</code><button aria-label="Copy example attach command" onClick={async()=>{try{await navigator.clipboard.writeText(command);setCopied(true);setTimeout(()=>setCopied(false),1800)}catch{setCopied(false)}}}>{copied?<Check size={15}/>:<Copy size={15}/>}</button></div>
  <div className="map-caption"><span>Choose a host to see its command.</span><span>Your system Tailscale stays untouched <ArrowUpRight size={12}/></span></div>
 </div>
}
