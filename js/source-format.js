// Convert stored source HTML into the editor's supported formatting syntax.
// Never insert source HTML into the live DOM.
const markers={B:'**',STRONG:'**',I:'__',EM:'__',U:'++',S:'~~',DEL:'~~',CODE:'`',PRE:'`'};
export function sourceEditorText(item,shown){
    const plain=String(shown??'').trim();
    const original=String(item?.text_original??item?.text??'').trim();
    if(!item?.text_html||plain!==original)return {text:plain,styled:false};
    const root=new DOMParser().parseFromString(item.text_html,'text/html').body;
    let styled=false;
    const walk=node=>{
        if(node.nodeType===3)return node.nodeValue||'';
        if(node.nodeType!==1||['SCRIPT','STYLE','TEMPLATE','IFRAME','OBJECT'].includes(node.tagName))return '';
        if(node.tagName==='BR')return '\n';
        const inner=[...node.childNodes].map(walk).join('');
        if(node.tagName==='A'){
            try{
                const u=new URL(node.getAttribute('href')||'');
                if(!['https:','http:'].includes(u.protocol)||!u.hostname||u.username||u.password||u.href.length>2048)return inner;
                const href=u.href.replace(/\(/g,'%28').replace(/\)/g,'%29');
                const label=(node.textContent||href).replace(/\[/g,'［').replace(/\]/g,'］').replace(/\s*\n\s*/g,' ');
                if(label===href)return href;
                styled=true;return '['+label+']('+href+')';
            }catch{return inner;}
        }
        const mark=markers[node.tagName];
        if(mark&&inner.trim()){
            // The delivery grammar supports non-nested inline spans, one line at a time.
            styled=true;
            return inner.split('\n').map(line=>line.trim()?mark+line+mark:line).join('\n');
        }
        return ['P','DIV','LI','BLOCKQUOTE'].includes(node.tagName)?inner+'\n':inner;
    };
    const text=[...root.childNodes].map(walk).join('').trim();
    return {text:text||plain,styled};
}
export function plainStyledText(text){
    return String(text||'').replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g,(_,label,url)=>label===url?url:label+' ('+url+')')
        .replace(/\*\*([^*\n]+)\*\*|__([^_\n]+)__|\+\+([^+\n]+)\+\+|~~([^~\n]+)~~|\x60([^\x60\n]+)\x60/g,(_, ...groups)=>groups.slice(0,5).find(v=>v!==undefined)||'');
}
