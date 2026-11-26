// Convert stored source HTML into the editor's supported formatting syntax.
// Never insert source HTML into the live DOM.
const markers={B:'**',STRONG:'**',I:'__',EM:'__',U:'++',S:'~~',DEL:'~~',CODE:'`',PRE:'`'};
export function sourceEditorText(item,shown){
    const plain=String(shown??'').trim();
    const original=String(item?.text_original??item?.text??'').trim();
    if(!item?.text_html||plain!==original)return {text:plain,styled:false};
    const root=new DOMParser().parseFromString(item.text_html,'text/html').body;
    let styled=false;
    const parts=[];
    const append=(value,mark='')=>{
        if(!value)return;
        const last=parts[parts.length-1];
        if(last&&last.mark===mark&&!last.link)last.value+=value;
        else parts.push({value,mark});
    };
    const walk=(node,mark='')=>{
        if(node.nodeType===3){append(node.nodeValue||'',mark);return;}
        if(node.nodeType!==1||['SCRIPT','STYLE','TEMPLATE','IFRAME','OBJECT'].includes(node.tagName))return;
        if(node.tagName==='BR'){append('\n');return;}
        if(node.tagName==='A'){
            try{
                const u=new URL(node.getAttribute('href')||'');
                if(!['https:','http:'].includes(u.protocol)||!u.hostname||u.username||u.password||u.href.length>2048)throw Error();
                const href=u.href.replace(/\(/g,'%28').replace(/\)/g,'%29');
                const label=(node.textContent||href).replace(/\[/g,'［').replace(/\]/g,'］').replace(/\s*\n\s*/g,' ');
                // A link must stay a link even inside a bold or italic span.
                styled=true;
                parts.push({value:label===href?href:'['+label+']('+href+')',link:true});
                return;
            }catch{/* Preserve the visible label, not an unsafe URL. */}
        }
        for(const child of node.childNodes)walk(child,markers[node.tagName]||mark);
        if(['P','DIV','LI','BLOCKQUOTE'].includes(node.tagName))append('\n');
    };
    for(const child of root.childNodes)walk(child);
    const text=parts.map(({value,mark})=>{
        if(!mark||!value.trim())return value;
        styled=true;
        return value.split('\n').map(line=>line.replace(/^(\s*)(.*?\S)(\s*)$/,(_,a,b,c)=>a+mark+b+mark+c)).join('\n');
    }).join('').trim();
    return {text:text||plain,styled};
}
export function plainStyledText(text){
    return String(text||'').replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g,(_,label,url)=>label===url?url:label+' ('+url+')')
        .replace(/\*\*([^*\n]+)\*\*|__([^_\n]+)__|\+\+([^+\n]+)\+\+|~~([^~\n]+)~~|\x60([^\x60\n]+)\x60/g,(_, ...groups)=>groups.slice(0,5).find(v=>v!==undefined)||'');
}
