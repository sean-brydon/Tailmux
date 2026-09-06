import { source } from '@/lib/source';
import { notFound } from 'next/navigation';
import { DocsPage, DocsBody, DocsTitle, DocsDescription } from 'fumadocs-ui/page';
import { getMDXComponents } from '@/components/mdx';
export default async function Page({params}:{params:Promise<{slug?:string[]}>}) {
 const {slug}=await params;
 const page=source.getPage(slug);
 if(!page) notFound();
 const MDX=page.data.body;
 const overview=!slug?.length;
 return <DocsPage toc={page.data.toc} full={false} editOnGithub={{owner:'sean-brydon',repo:'Tailmux',sha:'main',path:`docs/content/docs/${slug?.join('/') || 'index'}.mdx`}}>
  <div className="page-eyebrow">{overview?'GETTING STARTED':'TAILMUX DOCUMENTATION'}</div>
  <DocsTitle className={overview?'overview-title':''}>{overview ? <>Your machines.<br/>One command away.</> : page.data.title}</DocsTitle>
  <DocsDescription className={overview ? "overview-description" : undefined}>{page.data.description}</DocsDescription>
  <DocsBody><MDX components={getMDXComponents()}/></DocsBody>
 </DocsPage>
}
export function generateStaticParams(){return source.generateParams();}
export async function generateMetadata({params}:{params:Promise<{slug?:string[]}>}){const {slug}=await params;const page=source.getPage(slug);return {title:page?.data.title,description:page?.data.description};}
