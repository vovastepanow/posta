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


// The article reader returns ordered blocks, not just the RSS lead.
export function articleEditorContent(article, fallbackTitle=''){
    const parts=[],media=[];
    let styled=false,bodyFound=false;
    const urlOf=value=>{
        try{const u=new URL(String(value||''));return ['https:','http:'].includes(u.protocol)&&!u.username&&!u.password&&u.href.length<=2048?u.href.replace(/\(/g,'%28').replace(/\)/g,'%29'):'';}catch{return '';}
    };
    const links=(text,refs=[])=>{
        let rest=String(text||''),out='';
        for(const ref of refs||[]){
            if(!Array.isArray(ref))continue;
            const [label,raw]=ref,href=urlOf(raw);
            const at=label?rest.indexOf(label):-1;
            if(at<0||!href)continue;
            const name=String(label).replace(/\[/g,'［').replace(/\]/g,'］').replace(/\s*\n\s*/g,' ');
            out+=rest.slice(0,at)+(name===href?href:'['+name+']('+href+')');
            rest=rest.slice(at+label.length);styled=true;
        }
        return out+rest;
    };
    const heading=text=>{const value=String(text||'').trim();if(value){styled=true;parts.push('**'+value.replace(/\n+/g,' ')+'**');}};
    heading(article?.title||fallbackTitle);
    for(const block of article?.blocks||[]){
        switch(block.type){
        case 'h': if(block.text){heading(block.text);bodyFound=true;}break;
        case 'p':case 'quote':
            if(block.text){parts.push(links(block.text,block.links));bodyFound=true;}break;
        case 'list':{
            const items=(block.items||[]).filter(i=>i.text).map(i=>'• '+links(i.text,i.links));
            if(items.length){parts.push(items.join('\n'));bodyFound=true;}break;
        }
        case 'image':{
            const src=urlOf(block.src);if(src&&!media.some(m=>m.src===src))media.push({kind:'image',src,thumb:src,name:'Изображение статьи'});
            if(block.caption)parts.push(String(block.caption));break;
        }
        case 'video':{
            const href=urlOf(block.url);if(href)parts.push(links(block.title||'Видео из статьи',[[block.title||'Видео из статьи',href]]));break;
        }
        case 'embed':{
            const value=[block.title,block.description].filter(Boolean).join('\n');if(value)parts.push(value);break;
        }
        }
    }
    const cover=urlOf(article?.cover?.src);
    if(cover&&!media.some(m=>m.src===cover))media.unshift({kind:'image',src:cover,thumb:cover,name:'Обложка статьи'});
    return {text:parts.join('\n\n'),styled,media,bodyFound};
}

// Identify the record, never its thumbnail alone. A source post identity
// survives refreshed media URLs. A synthesized viewer item has no post ID,
// so include its actual video/image and text as the final fallback.
export function editorRecordKey(item) {
    const source=String(item?.source_key||'');
    const post=String(item?.post_id||'');
    const page=String(item?.source_url||item?.url||item?.video_page||'');
    if(source&&post)return 'record:'+JSON.stringify([source,post,item?.thread_id||'']);
    if(page)return 'page:'+page;
    return 'content:'+JSON.stringify([item?.item_key||'',item?.video||item?.display_video||'',item?.image||item?.display_image||'',item?.text_original??item?.text??'',item?.text_html||'']);
}
