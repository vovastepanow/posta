import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';

const root=path.resolve(process.argv[2]||'.');
const server=createServer(async(req,res)=>{
    try{
        const p=new URL(req.url,'http://localhost').pathname;
        if(p==='/'){res.setHeader('Content-Type','text/html; charset=utf-8');return res.end('<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="/css/app.css"></head><body></body></html>');}
        if(p==='/home-test'){
            const html=(await readFile(path.join(root,'index.html'),'utf8')).replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi,'').replace('class="app-loading"','class="app-loading is-done"');
            res.setHeader('Content-Type','text/html; charset=utf-8');return res.end(html);
        }
        if(!/^\/(js|css)\/[\w./-]+\.(js|css)$/.test(p)||p.includes('..')){res.writeHead(404);return res.end();}
        res.setHeader('Content-Type',p.endsWith('.js')?'text/javascript':'text/css');res.end(await readFile(path.join(root,p)));
    }catch{res.writeHead(404);res.end();}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const browser=await chromium.launch();
try{
    const page=await browser.newPage({viewport:{width:1440,height:900}});
    const errors=[];page.on('pageerror',e=>errors.push(e.message));
    await page.goto('http://127.0.0.1:'+server.address().port);
    await page.evaluate(async()=>{
        const {openPostEditor}=await import('/js/editor.js');
        const {sourceEditorText,articleEditorContent}=await import('/js/source-format.js');
        const full=articleEditorContent({title:'Полная статья',lead:'Короткий анонс',blocks:[{type:'p',text:'Первый абзац'},{type:'h',text:'Раздел'},{type:'list',items:[{text:'Ссылка',links:[['Ссылка','https://example.com/a']]}]},{type:'image',src:'https://example.com/image.jpg',caption:'Подпись'},{type:'p',text:'Последний абзац'}]});
        if(!full.bodyFound||!full.text.endsWith('Последний абзац')||!full.text.includes('[Ссылка](https://example.com/a)')||full.text.includes('Короткий анонс')||full.media.length!==1)throw Error('Full article import failed');
        const {draftPayload,appendEditorMedia}=await import('/js/editor-variants.js');
        const photo=new File(['photo'],'photo.jpg',{type:'image/jpeg'});
        const cover=new File(['cover'],'cover.jpg',{type:'image/jpeg'});
        const attachments=[
            {kind:'image',name:'photo.jpg',file:photo,existing:3},
            {kind:'video',name:'remote.mp4',src:'https://example.com/remote.mp4',cover:{file:cover}},
            {kind:'video',name:'saved.mp4',src:'https://example.com/saved.mp4',existing:0,cover:{existing:true}}
        ];
        const editForm=new FormData();
        if(appendEditorMedia(editForm,attachments,{allowExisting:true})!==2)throw Error('Incorrect multipart file count');
        const expectedEdit=[{kind:'image',name:'photo.jpg',upload:0},{kind:'video',name:'remote.mp4',src:'https://example.com/remote.mp4',cover_upload:1},{kind:'video',name:'saved.mp4',existing:0,cover_existing:true}];
        if(JSON.stringify(JSON.parse(editForm.get('media')))!==JSON.stringify(expectedEdit)||editForm.getAll('files[]').map(f=>f.name).join(',')!=='photo.jpg,cover.jpg')throw Error('Queue attachment manifest or upload order lost');
        const newForm=new FormData();
        appendEditorMedia(newForm,attachments);
        const expectedNew=[expectedEdit[0],expectedEdit[1],{kind:'video',name:'saved.mp4',src:'https://example.com/saved.mp4'}];
        if(JSON.stringify(JSON.parse(newForm.get('media')))!==JSON.stringify(expectedNew))throw Error('Queue-only reference leaked to a new publication');
        const imported=sourceEditorText({text:'Bold\nRead',text_html:'<b>Bold</b><br><a href="https://example.com/a">Read</a>'},'Bold\nRead');
        if(imported.text!=='**Bold**\n[Read](https://example.com/a)'||!imported.styled)throw Error('Rich source import failed');
        const nested=sourceEditorText({text:'Read now',text_html:'<b><a href="https://example.com/a">Read</a> now</b>'},'Read now');
        if(nested.text!=='[Read](https://example.com/a) **now**')throw Error('Nested link lost');
        const fallback=draftPayload({text:imported.text,extras:{styled_text:true}},{value:'vk:2',capabilities:{extras:[]}});
        if(fallback.text!=='Bold\nRead (https://example.com/a)'||fallback.extras.styled_text)throw Error('Plain source fallback failed');
        const tg={value:'tg:main',label:'Telegram'};
        const vk={value:'vk:2',label:'VK',capabilities:{platform:'vk',kinds:['photo','video','document'],mediaMax:10,textLimit:[16384,16384],poll:true,pollLimits:{question_max:300,options_max:10,option_max:100,multiple:true,with_content:true,media_max:9},extras:['first_comment','close_comments']}};
        window.result=null;
        openPostEditor({key:'browser-test',targets:[tg,vk],picked:[tg.value,vk.value],media:[],original:'Общий текст',onSubmit:async post=>{window.result=post;}});
    });
    const text=page.locator('.pe-text');
    await text.waitFor({state:'visible'});
    assert.equal(await text.inputValue(),'Общий текст');
    assert.equal(await page.locator('.pe-demo-error').count(),0);
    assert.equal(await page.locator('.pe-settings-column .pe-target-option').count(),2);
    assert.equal(await page.locator('.pe-compose-column .pe-attachment-section').count(),1);
    await page.locator('.pe-settings-open').click();
    assert.equal(await page.locator('.pe-extra-section').isVisible(),true);
    await page.getByRole('button',{name:'← Назад',exact:true}).click();
    assert.equal(await page.locator('.pe-extra-section').isVisible(),false);
    await page.setViewportSize({width:975,height:899});
    await page.waitForTimeout(100);
    const compact=await page.locator('.pe-window').evaluate(e=>[...e.querySelectorAll('.pe-compose-column,.pe-settings-column,.pe-variant-bar')].map(n=>({name:n.className,client:n.clientHeight,scroll:n.scrollHeight,width:n.clientWidth,scrollWidth:n.scrollWidth})));
    assert.ok(compact.every(n=>n.scroll<=n.client+2&&n.scrollWidth<=n.width+2),JSON.stringify(compact));
    await page.setViewportSize({width:1440,height:900});
    const desktop=await page.locator('.pe-window').evaluate(e=>({height:e.getBoundingClientRect().height,bodyOverflow:getComputedStyle(e.querySelector('.pe-body')).overflowY,left:e.querySelector('.pe-compose-column').getBoundingClientRect().left,right:e.querySelector('.pe-settings-column').getBoundingClientRect().left,footer:e.querySelector('.pe-footer').getBoundingClientRect().bottom,bottom:e.getBoundingClientRect().bottom}));
    assert.ok(desktop.height>=830&&desktop.bodyOverflow==='hidden'&&desktop.left<desktop.right&&desktop.footer<=desktop.bottom,JSON.stringify(desktop));
    assert.equal(await page.locator('.pe-checks').isVisible(),false);
    assert.equal(await page.locator('.pe-attachment-section h3').first().textContent(),'Вложения');
    assert.equal(await page.locator('.pe-attachment-section .pe-chip-poll').count(),1);
    assert.equal(await page.locator('.pe-chips .pe-chip-poll').count(),0);
    await page.locator('.pe-variant-tab').filter({hasText:'Telegram'}).click();
    await page.locator('.pe-attachment-section [data-extra-card=location]').click();
    assert.equal(await text.isVisible(),false);
    assert.equal(await page.locator('.pe-attachment-section').isVisible(),true);
    assert.equal(await page.locator('.pe-checks').isVisible(),true);
    await page.getByLabel('Широта',{exact:true}).fill('60');
    await page.getByLabel('Долгота',{exact:true}).fill('24');
    assert.equal(await page.locator('.pe-checks').isVisible(),false);
    await page.locator('.pe-attachment-section [data-extra-card=location]').click();
    assert.equal(await text.inputValue(),'Общий текст');
    await page.locator('.pe-attachment-section [data-extra-card=contact]').click();
    assert.equal(await page.locator('.pe-checks').isVisible(),true);
    await page.getByLabel('Имя',{exact:true}).fill('Тест');
    await page.getByLabel('Телефон +…',{exact:true}).fill('+3581234567');
    assert.equal(await page.locator('.pe-checks').isVisible(),false);
    await page.locator('.pe-attachment-section [data-extra-card=contact]').click();
    assert.equal(await text.inputValue(),'Общий текст');
    await text.fill('Telegram text');
    await text.evaluate(t=>t.setSelectionRange(0,8));
    await page.locator('.pe-format-action').first().click();
    assert.equal(await page.locator('.pe-format-preview b').textContent(),'Telegram');
    await page.locator('.pe-window input[type=file]').first().setInputFiles({name:'clip.mp4',mimeType:'video/mp4',buffer:Buffer.from('video fixture')});
    await page.locator('.pe-card input[type=file]').setInputFiles({name:'cover.png',mimeType:'image/png',buffer:Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jXxkAAAAASUVORK5CYII=','base64')});
    await page.locator('.pe-cover-remove').waitFor({state:'visible'});
    assert.ok((await page.locator('.pe-card video').getAttribute('poster')).startsWith('blob:'));
    await page.locator('.pe-variant-tab').filter({hasText:'VK'}).click();
    assert.equal(await text.inputValue(),'Общий текст');
    await text.fill('VK text');
    await page.locator('.pe-chip-poll').click();
    await page.locator('.pe-poll-options').waitFor({state:'visible'});
    assert.equal(await page.locator('.pe-attachment-section .pe-poll-options').isVisible(),true);
    assert.equal(await page.locator('.pe-checks').isVisible(),true);
    await page.getByLabel('Добавить текст и медиа к опросу').check();
    await page.getByPlaceholder('Вопрос опроса').fill('Вопрос?');
    await page.locator('.pe-poll-options input').nth(0).fill('Да');
    await page.locator('.pe-poll-options input').nth(1).fill('Нет');
    assert.equal(await text.inputValue(),'VK text');
    await page.locator('.pe-variant-tab').filter({hasText:'Общая'}).click();
    assert.equal(await text.inputValue(),'Общий текст');
    assert.equal(await page.locator('.pe-btn-primary').isEnabled(),true);
    assert.equal(await page.locator('.pe-checks').isVisible(),false);
    await page.setViewportSize({width:390,height:844});
    const bounds=await page.locator('.pe-window').evaluate(e=>({width:e.getBoundingClientRect().width,scroll:e.scrollWidth,client:e.clientWidth,body:e.querySelector('.pe-body').scrollWidth,bodyClient:e.querySelector('.pe-body').clientWidth}));
    assert.ok(bounds.width<=391&&bounds.scroll<=bounds.client+2&&bounds.body<=bounds.bodyClient+2,JSON.stringify(bounds));
    assert.equal(await page.locator('.pe-demo-error').count(),0);
    await page.locator('.pe-btn-primary').click();
    await page.waitForFunction(()=>window.result!==null);
    const post=await page.evaluate(()=>window.result);
    assert.equal(post.payloads['tg:main'].text,'**Telegram** text');
    assert.equal(post.payloads['tg:main'].extras.styled_text,true);
    assert.equal(post.payloads['tg:main'].media[0].cover.name,'cover.png');
    assert.equal(post.payloads['vk:2'].media.length,0);
    assert.equal(post.payloads['vk:2'].text,'VK text');
    assert.equal(post.payloads['vk:2'].poll.with_content,true);
    assert.deepEqual(post.payloads['vk:2'].poll.options,['Да','Нет']);
    // A long poll in a short mobile viewport must not make the last
    // option inaccessible. A selected pane can scroll only as a last resort.
    await page.setViewportSize({width:390,height:500});
    await page.evaluate(async()=>{
        const {openPostEditor}=await import('/js/editor.js');
        const tg={value:'tg:main',label:'Telegram'};
        openPostEditor({key:'short-poll-test',targets:[tg],picked:[tg.value],media:[],original:'',poll:{question:'Long poll?',options:Array.from({length:10},(_,i)=>'Option '+(i+1)),multiple:false},onSubmit:async()=>{}});
    });
    const pane=page.locator('.pe-compose-column');
    await page.waitForFunction(()=>document.querySelector('.pe-compose-column')?.dataset.overflow==='true');
    assert.equal(await pane.evaluate(n=>getComputedStyle(n).overflowY),'auto');
    const lastOption=page.getByPlaceholder('Вариант 10',{exact:true});
    await lastOption.scrollIntoViewIfNeeded();
    await lastOption.fill('Last reachable option');
    assert.equal(await lastOption.inputValue(),'Last reachable option');
    await page.waitForFunction(()=>{const n=document.querySelector('input[placeholder="Вариант 10"]');if(!n)return false;const r=n.getBoundingClientRect(),p=n.closest('.pe-compose-column').getBoundingClientRect();return r.top>=p.top-2&&r.bottom<=p.bottom+2;},{},{timeout:5000});
    const previewPage=await browser.newPage({viewport:{width:900,height:700}});
    previewPage.on('pageerror',e=>errors.push(e.message));
    await previewPage.goto('http://127.0.0.1:'+server.address().port);
    await previewPage.evaluate(async()=>{
        const {fromRecord}=await import('/js/post-adapters.js');
        const {openPostView}=await import('/js/post-view.js');
        const post=fromRecord({text:'Проверяем общий просмотр',source:'Telegram | Example',source_url:'https://t.me/example/1',why_relevant:'Соответствует теме'});
        window.previewAction=0;
        openPostView([post],0,{actions:()=>[{label:'Тестовое действие',run:()=>{window.previewAction++;}}]});
    });
    await previewPage.locator('.pv').waitFor({state:'visible'});
    assert.equal(await previewPage.locator('.pv-source-name').textContent(),'Example');
    assert.ok((await previewPage.locator('.pv-body').textContent()).includes('Почему отобран: Соответствует теме'));
    await previewPage.getByRole('button',{name:'Тестовое действие'}).click();
    assert.equal(await previewPage.evaluate(()=>window.previewAction),1);
    await previewPage.keyboard.press('Escape');
    await previewPage.locator('.pv').waitFor({state:'hidden'});
    await previewPage.close();
    // Load the actual home module with the real markup and a deterministic API.
    const homePage=await browser.newPage({viewport:{width:1440,height:900}});
    homePage.on('pageerror',e=>errors.push(e.message));
    await homePage.route('**/*',route=>{
        const url=new URL(route.request().url());
        if(url.hostname!=='127.0.0.1')return route.abort();
        if(url.pathname!=='/index.php')return route.continue();
        const fixtures={
            proposals:{items:[
                {item_key:'text',text:'Текстовый пост',text_post:true,timestamp:3,source:'Telegram | Example',why_relevant:'Соответствует теме'},
                {item_key:'photo',text:'Фото поста',display_image:'/fixture.png',timestamp:2},
                {item_key:'video',text:'Видео поста',video_via:'ytdlp',video_pending:true,timestamp:1}
            ]},
            account_info:{user_id:'test-home',auth:{login:'Test'}},
            topic_sources:{sources:[]},
            studio_live:{items:[],pool:3,running:false},
            billing_info:{plan:{title:'Тест'}},
        };
        return route.fulfill({json:fixtures[url.searchParams.get('action')]||{}});
    });
    await homePage.goto('http://127.0.0.1:'+server.address().port+'/home-test');
    await homePage.evaluate(async()=>{
        const {initializeCore}=await import('/js/core.js');
        initializeCore();
        await import('/js/home.js');
    });
    await homePage.waitForFunction(()=>document.querySelectorAll('#grid .tile').length===3);
    assert.equal(await homePage.locator('#grid .rejected-reason').count(),0);
    assert.equal(await homePage.getByRole('button',{name:'Убрать из отобранных',exact:true}).count(),3);
    await homePage.locator('#chips button').filter({hasText:'Видео'}).click();
    assert.equal(await homePage.locator('#grid .tile').count(),1);
    assert.ok((await homePage.locator('#grid').textContent()).includes('Видео поста'));
    await homePage.locator('#chips button').filter({hasText:'Текст'}).click();
    assert.equal(await homePage.locator('#grid .tile').count(),1);
    await homePage.locator('#grid .tile b').click();
    await homePage.locator('.pv').waitFor({state:'visible'});
    assert.ok((await homePage.locator('.pv-body').textContent()).includes('Почему отобран: Соответствует теме'));
    await homePage.keyboard.press('Escape');
    await homePage.locator('.pv').waitFor({state:'hidden'});
    await homePage.close();
    // Refreshing source status must preserve an opened, already loaded history.
    const sourcesPage=await browser.newPage({viewport:{width:1440,height:900}});
    sourcesPage.on('pageerror',e=>errors.push(e.message));
    let statusReads=0, historyReads=0;
    await sourcesPage.route('**/*',route=>{
        const url=new URL(route.request().url());
        if(url.hostname!=='127.0.0.1')return route.abort();
        if(url.pathname!=='/index.php')return route.continue();
        const action=url.searchParams.get('action');
        if(action==='source_autopilot_status')statusReads++;
        if(action==='source_autopilot_history')historyReads++;
        const fixtures={
            topics:{topics:[{title:'SMM и блогинг',path:'SMM и блогинг'}]},
            source_autopilot_status:{server_revision:'test',auto_enabled:true,topics:[{topic:'SMM и блогинг',mode:'auto',posts_per_day:10,horizon_days:3,attempts:statusReads}]},
            source_autopilot_history:{decisions:[{source_id:'rss:example.org',new_state:'candidate',reason:'discovered',at:1}]},
            topic_sources:{sources:[]},mirror_list:{mirrors:[]},telegram_queue:{connected:false}
        };
        return route.fulfill({json:fixtures[action]||{}});
    });
    await sourcesPage.goto('http://127.0.0.1:'+server.address().port+'/home-test');
    await sourcesPage.evaluate(async()=>{
        const {initializeCore,switchTab}=await import('/js/core.js');
        initializeCore();
        const {initializeSourceTab}=await import('/js/sources.js');
        initializeSourceTab();
        switchTab('source-tab');
    });
    const history=sourcesPage.locator('#autopilot-topics details[data-autopilot-history]');
    await history.locator('summary').click();
    await history.getByText('rss:example.org:',{exact:false}).waitFor();
    await sourcesPage.evaluate(async()=>{const {switchTab}=await import('/js/core.js');switchTab('source-tab');});
    await sourcesPage.waitForFunction(()=>document.querySelector('#autopilot-topics')?.textContent.includes('Запусков задания: 2'));
    assert.equal(await history.evaluate(e=>e.open),true);
    assert.equal(historyReads,1);
    await sourcesPage.close();
    // A refresh while an edit/confirmation is open must never redirect a write
    // to the new record at the same row index.
    const dbPage = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    dbPage.on('pageerror', e => errors.push(e.message));
    let dbReads = 0;
    const writes = [];
    await dbPage.route('**/*', route => {
        const url = new URL(route.request().url());
        if (url.hostname !== '127.0.0.1') return route.abort();
        if (url.pathname !== '/index.php') return route.continue();
        const action = url.searchParams.get('action');
        if (action === 'database_options') return route.fulfill({ json: {
            databases: [{ id: 'content_pool', label: 'Пул', by_topic: true, collections: [{ id: 'items', label: 'Записи' }] }]
        }});
        if (action === 'database_read') return route.fulfill({ json: {
            database_label: 'Пул', collection_label: 'Записи', topic: 'SMM',
            items: [{ image: 'https://example.com/image.jpg', text: 'Record ' + (++dbReads) }]
        }});
        if (['database_update', 'database_delete', 'database_add'].includes(action)) {
            writes.push({ action, body: route.request().postDataJSON() });
        }
        return route.fulfill({ json: {} });
    });
    await dbPage.goto('http://127.0.0.1:' + server.address().port + '/home-test');
    await dbPage.evaluate(async () => {
        const { initializeCore } = await import('/js/core.js');
        initializeCore();
        const { initializeDatabases, loadDatabaseOptions } = await import('/js/account-database.js');
        const tab = document.getElementById('admin-tab');
        tab.hidden = false;
        tab.classList.add('active');
        tab.querySelector('[data-admin-section=databases]').hidden = false;
        initializeDatabases();
        await loadDatabaseOptions();
    });
    await dbPage.locator('#database-list').getByText('Record 1').waitFor();
    await dbPage.locator('#database-edit').click();
    await dbPage.locator('#database-refresh').evaluate(button => button.click());
    await dbPage.locator('#database-list').getByText('Record 2').waitFor({ state: 'attached' });
    await dbPage.locator('#database-edit-form [type=submit]').click();
    await dbPage.locator('#database-dialog-status').getByText('Список изменился.', { exact: false }).waitFor();
    assert.equal(writes.length, 0);
    await dbPage.locator('#database-cancel').click();
    await dbPage.locator('#database-delete').click();
    await dbPage.locator('#database-refresh').evaluate(button => button.click());
    await dbPage.locator('#database-list').getByText('Record 3').waitFor({ state: 'attached' });
    await dbPage.locator('.ask-ok').click();
    await dbPage.locator('#database-status').getByText('Список изменился.', { exact: false }).waitFor();
    assert.equal(writes.length, 0);
    await dbPage.locator('#database-edit').click();
    await dbPage.locator('#database-record-json').fill(JSON.stringify({ image: 'https://example.com/image.jpg', text: 'Edited' }));
    await dbPage.locator('#database-edit-form [type=submit]').click();
    await dbPage.locator('#database-edit-dialog').waitFor({ state: 'hidden' });
    assert.equal(writes.length, 1);
    assert.deepEqual(writes[0], { action: 'database_update', body: {
        database: 'content_pool', collection: 'items', record: { image: 'https://example.com/image.jpg', text: 'Edited' },
        expected_topic: 'SMM', index: 0, expected: { image: 'https://example.com/image.jpg', text: 'Record 3' }
    }});
    await dbPage.close();

    const poolPage = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    poolPage.on('pageerror', e => errors.push(e.message));
    let poolReads = 0, planReads = 0;
    await poolPage.route('**/*', route => {
        const url = new URL(route.request().url());
        if (url.hostname !== '127.0.0.1') return route.abort();
        if (url.pathname !== '/index.php') return route.continue();
        const action = url.searchParams.get('action');
        if (action === 'proposals') {
            poolReads++;
            return route.fulfill({ json: { items: [
                { item_key: 'one', text: 'First post', format: 'text' },
                { item_key: 'two', text: 'Video post', format: 'video' }
            ] } });
        }
        if (action === 'calendar_fill_plan') {
            planReads++;
            return route.fulfill({ json: { plan: [] } });
        }
        return route.fulfill({ json: {} });
    });
    await poolPage.goto('http://127.0.0.1:' + server.address().port);
    await poolPage.evaluate(async () => {
        const { renderPoolColumn, holesChildren, fetchPlan } = await import('/js/calendar-pool.js');
        const root = document.createElement('section');
        root.id = 'pool-test';
        document.body.append(root);
        const ctx = { targets: () => ['channel'], openFill: () => {} };
        const plan = fetchPlan(ctx.targets());
        await Promise.all([renderPoolColumn(ctx, root, plan), holesChildren(ctx, { queued: 0, free: 0 }, plan)]);
    });
    assert.equal(poolReads, 1);
    assert.equal(planReads, 1);
    assert.equal(await poolPage.locator('#pool-test .cal-pool-item').count(), 2);
    await poolPage.locator('#pool-test .cal-pool-filters button').filter({ hasText: 'Видео' }).click();
    assert.equal(await poolPage.locator('#pool-test .cal-pool-item').count(), 1);
    assert.equal(await poolPage.locator('#pool-test .cal-pool-filters button').filter({ hasText: 'Видео' }).getAttribute('aria-pressed'), 'true');
    await poolPage.locator('#pool-test .cal-pool-toggle').click();
    await poolPage.locator('#pool-test .cal-pool-toggle').click();
    assert.equal(poolReads, 1);
    assert.equal(planReads, 1);
    await poolPage.close();
    // Older network list responses and completed connection attempts must not
    // replace an authoritative resource list or clear the next setup form.
    const networkPage = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    networkPage.on('pageerror', e => errors.push(e.message));
    await networkPage.goto('http://127.0.0.1:' + server.address().port + '/home-test');
    await networkPage.evaluate(async () => {
        const { loadNetworks } = await import('/js/networks.js');
        const networks = Object.fromEntries(['vk', 'ok'].map(key => [key, {
            title: key, publishing_in_scope: true, connect_available: true, status: 'ready',
            fields: [{name:'token', label:'Токен', secret:true}]
        }]));
        const old = {networks, channels:[], inactive_channels:[]};
        let reads = 0, completeRead, completeWrite;
        window.fetch = async (raw) => {
            const action = new URL(raw, location.href).searchParams.get('action');
            let data = {};
            if (action === 'networks_list') {
                data = ++reads === 1 ? old : await new Promise(resolve => { completeRead = resolve; });
            }
            if (action === 'network_connect') data = await new Promise(resolve => { completeWrite = resolve; });
            return new Response(JSON.stringify(data), {status:200, headers:{'Content-Type':'application/json'}});
        };
        await loadNetworks();
        const stale = loadNetworks();
        const buttons = [...document.querySelectorAll('#networks-list button')];
        buttons[0].click();
        const form = document.getElementById('networks-form');
        form.querySelector('input').value = 'fixture-first';
        const pending = form.onsubmit({preventDefault(){}, submitter:form.querySelector('[type=submit]')});
        buttons[1].click();
        form.querySelector('input').value = 'fixture-second';
        completeWrite({channels:[{id:1, platform:'vk', title:'New resource'}], inactive_channels:[]});
        await pending;
        completeRead(old);
        await stale;
        if (document.getElementById('networks-count').textContent !== '1') throw Error('Stale network list replaced connected resources');
        if (form.hidden || form.querySelector('input').value !== 'fixture-second') throw Error('Old mutation cleared the new setup form');
    });
    await networkPage.close();
    assert.deepEqual(errors,[]);
    console.log('Editor browser: isolated variants, formatting, compound poll, mobile layout and submitted payloads passed');
}finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
