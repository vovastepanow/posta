#!/usr/bin/env python3
"""Bounded free-only diagnostic; never writes classification labels."""
import os,json,base64,urllib.request,urllib.error,time,hashlib
from pathlib import Path
root=Path('/var/www/html/posta');out=root/'data/backups/classification-free-real-20261113'
def save(name,obj):
 fd=os.open(out/name,os.O_CREAT|os.O_WRONLY|os.O_TRUNC,0o600)
 with os.fdopen(fd,'w') as f:json.dump(obj,f,ensure_ascii=False,indent=2)
key=os.environ.get('OPENROUTER_API_KEY','');url=os.environ.get('OPENROUTER_URL','')
for line in (root/'posta.env').read_text().splitlines():
 if '=' not in line or line.lstrip().startswith('#'):continue
 k,v=line.split('=',1);v=v.strip().strip('"').strip("'")
 if k.strip()=='OPENROUTER_API_KEY' and not key:key=v
 if k.strip()=='OPENROUTER_URL' and not url:url=v
if not key:raise SystemExit('OpenRouter key unavailable')
url=(url or 'https://openrouter.ai/api/v1').rstrip('/')
tree=json.load(open(root/'data/backups/classification-benchmark-v2-20261112/taxonomy.json'))
menu='\n'.join(t['key']+' — '+t['title']+': '+t.get('about','')+' '+t.get('hint','') for t in tree['leaves'])
texts=[json.loads(x) for x in open(root/'data/backups/classification-new-text-check-20261113/snapshot.jsonl')]
cases=[{'kind':'text','caption':x['data']['text'],'previous':x['tags'],'key':x['key']} for x in texts]
for name,kind in [('new-image','image'),('previously-untested-video','video')]:
 d=json.load(open(root/'data/backups/classification-multimedia-20261113'/f'{name}.json'));d['kind']=kind;cases.append(d)
save('plan.json',{'model':'nvidia/nemotron-3.5-super-vl:free','max_calls':4,'paid':False,'cases':[{k:v for k,v in c.items() if k!='media_file'} for c in cases],'mode':'independent_free_diagnostic_not_production_pipeline','taxonomy_sha256':hashlib.sha256(menu.encode()).hexdigest()})
responses=[]
for i,c in enumerate(cases,1):
 prompt='Определи темы самой публикации по тексту и приложенному полному медиа, не по профессии или отрасли автора. Выбирай только раскрытые темы из меню, до двух; вторую — только если ей посвящена существенная часть содержания. Если нет подходящей темы или содержания недостаточно, укажи неопределённость. Не ориентируйся на прежние метки. Верни JSON объект {"topics":["ключ темы"],"reason":"объяснение по содержанию","sufficient":true}.\nМЕНЮ:\n'+menu+'\nТЕКСТ ПУБЛИКАЦИИ:\n'+c['caption']
 content=[{'type':'input_text','text':prompt}]
 if c['kind'] in ('image','video'):
  raw=(root/c['media_file']).read_bytes() if not Path(c['media_file']).is_absolute() else Path(c['media_file']).read_bytes()
  enc=base64.b64encode(raw).decode();mime='image/png' if c['kind']=='image' else 'video/mp4'
  content.append({'type':'input_image' if c['kind']=='image' else 'input_video',('image_url' if c['kind']=='image' else 'video_url'):'data:'+mime+';base64,'+enc})
 payload={'model':'nvidia/nemotron-3.5-super-vl:free','input':[{'role':'user','content':content}],'max_output_tokens':1200,'provider':{'max_price':{'prompt':0,'completion':0}}}
 save(f'{i:02d}-request.json',payload)
 ledger={'physical_calls_started':i,'max_calls':4,'paid_usd':0};save('attempts.json',ledger)
 request=urllib.request.Request(url+'/responses',data=json.dumps(payload,ensure_ascii=False).encode(),headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'},method='POST')
 try:
  with urllib.request.urlopen(request,timeout=100) as r: data=json.load(r);status=r.status
 except urllib.error.HTTPError as e:
  status=e.code
  try:data=json.loads(e.read())
  except Exception:data={'error':'HTTP '+str(status)}
 except Exception as e:
  status=0;data={'error':type(e).__name__}
 save(f'{i:02d}-response.json',data)
 textsout=[part.get('text','') for item in data.get('output',[]) for part in item.get('content',[]) if part.get('type')=='output_text']
 row={'kind':c['kind'],'key':c['key'],'previous':c['previous'],'http_status':status,'response_text':'\n'.join(textsout),'usage':data.get('usage')}
 responses.append(row);save('results.json',responses)
 print(c['kind'],'HTTP',status,'response',row['response_text'][:1000],flush=True)
 if status!=200:break
 if i<len(cases):time.sleep(10)
