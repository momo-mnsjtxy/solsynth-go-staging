#!/usr/bin/env python3
"""Rebuild static operation inventory from pinned, permitted source snapshots.
This is a deliberately conservative lexical inventory, not an ASP.NET router.
"""
from pathlib import Path
import collections, hashlib, json, re
ROOT=Path(__file__).resolve().parents[2]
SOURCES=ROOT/'inventory/operation-sources'
BASELINE='4ee2d0cebb9da8284a92483fee6dce795bf6947c'
URL=f'https://github.com/Solsynth/DysonNetwork/blob/{BASELINE}/'
INV=json.loads((ROOT/'inventory/dyson-contract-files.json').read_text())
FORTUNE='DysonNetwork.Passport/Account/FortuneSayingController.cs'
HTTP_RE=re.compile(r'\[Http(Get|Post|Put|Patch|Delete|Head|Options)\b[^\]]*\]')
METHOD_RE=re.compile(r'\bpublic\s+(?:(?:async|virtual|override|static|sealed)\s+)*([^;={}]*?)\b(\w+)\s*\(')

def mask_cs(s):
    # Preserve offsets/newlines; ignore comments and literal contents for balance checks.
    out=list(s); i=0; n=len(s)
    def blank(a,b):
        for k in range(a,b):
            if out[k]!='\n': out[k]=' '
    while i<n:
        if s.startswith('//',i):
            j=s.find('\n',i); j=n if j<0 else j; blank(i,j);i=j
        elif s.startswith('/*',i):
            j=s.find('*/',i+2);j=n if j<0 else j+2;blank(i,j);i=j
        elif s[i] in '\"\'':
            quote=s[i];verbatim=i>0 and s[i-1]=='@';j=i+1
            while j<n:
                if s[j]==quote:
                    if verbatim and j+1<n and s[j+1]==quote:j+=2;continue
                    j+=1;break
                if not verbatim and s[j]=='\\':j+=2
                else:j+=1
            blank(i,j);i=j
        else:i+=1
    return ''.join(out)

def balanced(mask,start,op,cl):
    count=0
    for i in range(start,len(mask)):
        if mask[i]==op:count+=1
        elif mask[i]==cl:
            count-=1
            if count==0:return i
    raise ValueError(f'unbalanced {op} at {start}')

def attributes_before(s,mask,end):
    pos=end
    while pos>0 and mask[pos-1].isspace():pos-=1
    stop=pos
    while pos>0 and mask[pos-1]==']':
        depth=1;pos-=2
        while pos>=0 and depth:
            if mask[pos]==']':depth+=1
            elif mask[pos]=='[':depth-=1
            pos-=1
        pos+=1
        while pos>0 and mask[pos-1].isspace():pos-=1
    return s[pos:stop]

def route_values(attrs):
    return re.findall(r'\[Route\([\s(]*"([^"\n]*)"',attrs)

def auth_values(attrs):
    return re.findall(r'\[(?:Authorize|AllowAnonymous|AskPermission|RequiredPermission)\b[^\]]*\]',attrs)

def family(path):
    service=path.split('/')[0].split('.')[-1].lower(); name=Path(path).stem; folder=path.split('/')[1]
    if service=='develop':
        if 'MiniApp' in name:return 'develop.miniapps'
        if 'Bot' in name:return 'develop.bots'
        if 'Board' in name:return 'develop.boards'
        if 'DevProject' in name:return 'develop.projects'
        if 'Quota' in name or name=='DeveloperController':return 'develop.developers'
        return 'develop.apps_products'
    if service=='messager':
        return 'messager.calls' if 'Realtime' in name else ('messager.messages' if name=='ChatController' else 'messager.rooms')
    if service=='passport':
        if name=='BadgesDiscoveryController':return 'passport.badges'
        if name=='FortuneSayingController':return 'passport.fortunes'
        if folder in ['Progression','NameChangeCard','Affiliation']:return 'passport.progression'
        if folder=='Realm' or 'Friends' in name:return 'passport.social_realms'
        if folder in ['Meet','Nearby','Nfc']:return 'passport.location_nfc'
        if folder in ['Ticket','Examination']:return 'passport.tickets_exams'
        if name=='NotableDaysController':return 'passport.notable_days'
        return 'passport.accounts_presence'
    if service=='sphere':
        if folder=='ActivityPub':return 'sphere.federation_discovery' if name in ['NodeInfoController','WebFingerController'] else 'sphere.federation'
        if any(x in name for x in ['Category','Tag']):return 'sphere.taxonomy'
        if folder=='Publisher':return 'sphere.publishers'
        if folder=='Live' or name=='AdsController':return 'sphere.live_ads'
        if folder in ['Reader','Translation','Autocompletion']:return 'sphere.content_utilities'
        if folder in ['Sticker','Survey']:return 'sphere.stickers_surveys'
        if folder=='Timeline':return 'sphere.timeline'
        return 'sphere.posts'
    if service=='wallet':
        if 'Stats' in name:return 'wallet.reporting'
        if 'Merchant' in name:return 'wallet.merchants'
        if 'Subscription' in name:return 'wallet.subscriptions_gifts'
        if name=='WalletProductController' or name=='WalletProductAdminController':return 'wallet.product_catalog'
        if name=='WalletController':return 'wallet.ledger'
        return 'wallet.orders_payments'
    raise ValueError(path)

def status_for(path,action,verb,routes):
    if path==FORTUNE:return 'embedded_corpus_permission_unresolved'
    if path.endswith('PostWatchController.cs') and any(p=='/api/posts/watch' for p in routes) and verb in ['GET','PUT']:return 'development_candidate_at_baseline'
    if path.endswith('CustomAppPublicController.cs') and action=='DiscoverApps':return 'synthetic_postgresql_candidate_at_baseline'
    if path.endswith('BadgesDiscoveryController.cs') or (path.endswith('WalletController.cs') and action=='GetCurrencyExchanges'):return 'recovered_local_handler_current_checkpoint'
    if path.endswith('MiniAppPublicController.cs') and action=='DownloadPackage':return 'synthetic_candidate_implemented_current_checkpoint'
    if path.endswith('BotAccountPublicController.cs') and action=='GetBotCommands':return 'synthetic_candidate_implemented_current_checkpoint'
    if path.endswith('WalletProductController.cs') and action=='GetCatalog':return 'synthetic_candidate_implemented_current_checkpoint'
    if path.endswith('NodeInfoController.cs') or (path.endswith('WebFingerController.cs') and action=='GetHostMeta'):return 'synthetic_candidate_implemented_current_checkpoint'
    return 'not_implemented_at_snapshot_baseline'

def parse_controller(item):
    path=item['path'];file=SOURCES/path
    if not file.exists() and path==FORTUNE:return json.loads((SOURCES/'fortune-operation-metadata.json').read_text())
    s=file.read_text();mask=mask_cs(s);classes=list(re.finditer(r'public\s+(?:(?:sealed|partial)\s+)?class\s+(\w+Controller)\b',s))
    assert classes,path
    records=[]
    for attr in HTTP_RE.finditer(s):
        if not mask[attr.start():attr.start()+2]=='[H':continue
        cl=max((c for c in classes if c.start()<attr.start()), key=lambda c:c.start())
        header=attributes_before(s,mask,cl.start());bases=route_values(header);assert len(bases)==1,(path,cl.group(1),bases)
        mm=next((m for m in METHOD_RE.finditer(s,attr.end()) if mask[m.start():m.start()+6]=='public'),None);assert mm,(path,attr.group())
        open_paren=s.find('(',mm.start());close_paren=balanced(mask,open_paren,'(',')')
        nxt=close_paren+1
        while nxt<len(s) and mask[nxt].isspace():nxt+=1
        if mask[nxt]=='{':end=balanced(mask,nxt,'{','}')+1
        elif mask.startswith('=>',nxt):end=s.index(';',nxt)+1
        else:raise ValueError((path,mm.group(),s[nxt:nxt+40]))
        attrs=attributes_before(s,mask,mm.start());body=s[nxt:end];signature=' '.join(s[mm.start():close_paren+1].split())
        action=mm.group(2);verb=attr.group(1).upper();am=re.match(r'\[Http\w+(?:\(\s*"([^"\n]*)")?',attr.group());template=am.group(1)
        method_routes=route_values(attrs)
        # Method Route attributes are alternatives; bare Http verbs inherit them.
        templates=[template] if template is not None else (method_routes or [''])
        routes=[]
        for base in bases:
            for t in templates:
                p=t[1:] if t.startswith('~/') else t if t.startswith('/') else base.rstrip('/')+'/'+t
                p='/'+p.lstrip('/');p=p.rstrip('/') or '/';p=p.replace('[controller]',cl.group(1)[:-10]).replace('[action]',action)
                routes.append(p)
        line=s.count('\n',0,attr.start())+1
        rec={
            'id':f"{path.split('/')[0].split('.')[-1].lower()}:{cl.group(1)}.{action}@{line}:{verb}",
            'service':path.split('/')[0].split('.')[-1], 'family':family(path),
            'controller':cl.group(1),'action':action,'http_method':verb,
            'attribute':attr.group(),'attribute_line':line,
            'controller_routes':bases,'action_template':template,
            'service_route_candidates':list(dict.fromkeys(routes)),
            'route_status':'static_candidate_not_runtime_verified','gateway_aliases':'not_verified',
            'source_path':path,'source_blob':item['sha'],'source_url':URL+path+f'#L{line}',
            'signature':signature,'action_end_line':s.count('\n',0,end)+1,
            'declared_authorization':{'controller':auth_values(header),'action':auth_values(attrs),'global_policy':'unresolved; no attribute does not establish public access'},
            'observed':{
                'literal_error_codes':sorted(set(re.findall(r'\bCode\s*=\s*"([^"\n]+)"',body))),
                'response_helpers':sorted(set(re.findall(r'\b(?:return\s+|=>\s*)(Ok|NotFound|BadRequest|Unauthorized|Forbid|NoContent|CreatedAtAction|Created|Accepted|File|Content|StatusCode|Redirect)\s*\(',body))),
                'explicit_status_numbers':sorted(set(map(int,re.findall(r'(?:\bStatus\s*=|StatusCode\s*\()\s*(\d{3})',body)))),
                'direct_db_members':sorted(set(re.findall(r'\b_?db\.(\w+)',body))),
                'current_user_reference':'CurrentUser' in body,
                'explicit_side_effect_signals':sorted(set(re.findall(r'\b(SaveChangesAsync|ExecuteUpdateAsync|ExecuteDeleteAsync|PublishAsync|SendPushNotificationToUserAsync|CreateCheckoutAsync|SendAsync|DeleteAsync|UploadAsync|IncrementDownloadCountAsync)\b',body)))
            },
            'implementation_status':status_for(path,action,verb,routes),
            'next_action':'Use this family plan; pin body dependencies/DTO shapes and implement synthetic contract tests before enabling real adapters.',
            'required_release_gates':['G-ROUTING','G-JSON','G-AUTH','G-RECOVERY']
        }
        if verb!='GET' or rec['observed']['current_user_reference'] or auth_values(header) or auth_values(attrs):rec['required_release_gates'].append('G-AUTHORITY')
        if rec['observed']['direct_db_members']:rec['required_release_gates'].append('G-SCHEMA')
        if path==FORTUNE:rec['required_release_gates'].append('G-CORPUS')
        records.append(rec)
    data={'path':path,'blob':item['sha'],'source_url':URL+path,'family':family(path),'static_http_attribute_count':len(records),'controller_classes':[c.group(1) for c in classes],'operations':records}
    if path==FORTUNE:(SOURCES/'fortune-operation-metadata.json').write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n')
    return data

controllers=[parse_controller(x) for x in INV['groups']['controllers']]
operations=[o for c in controllers for o in c['operations']]
assert len(operations)==818,len(operations)
assert len({o['id'] for o in operations})==len(operations)
# Omit the embedded third-party quotations from retained distributable snapshots.
if (SOURCES/FORTUNE).exists():(SOURCES/FORTUNE).unlink()

rpc_files=[]
for item in INV['groups']['rpc']:
    if item['path'].startswith('DysonNetwork.Shared.Tests/'):
        rpc_files.append({'path':item['path'],'blob':item['sha'],'status':'filename_is_test_not_service; not inspected','methods':[]});continue
    s=(SOURCES/item['path']).read_text();ms=[]
    for m in re.finditer(r'public\s+(?:async\s+)?override\s+([^\n{]+?)\b(\w+)\s*\(([^)]*)\)',s,re.S):
        ms.append({'name':m.group(2),'line':s.count('\n',0,m.start())+1,'signature':' '.join(m.group().split()),'wire_status':'not_established; protobuf schemas/descriptors/generated bindings excluded'})
    rpc_files.append({'path':item['path'],'blob':item['sha'],'source_url':URL+item['path'],'status':'licensed_implementation_inspected; transport_not_ported','methods':ms})

job_files=[];scheduled=[]
for item in INV['groups']['jobs']:
    s=(SOURCES/item['path']).read_text();is_config=item['path'].endswith('ScheduledJobsConfiguration.cs')
    job_files.append({'path':item['path'],'blob':item['sha'],'source_url':URL+item['path'],'kind':'scheduler_configuration' if is_config else 'job_implementation','status':'licensed_source_inspected; not_ported','entrypoint_lines':[s.count('\n',0,m.start())+1 for m in re.finditer(r'public\s+(?:async\s+)?Task\s+Execute\s*\(',s)]})
    if is_config:
        for m in re.finditer(r'q\.AddJob<([^>]+)>\(opts\s*=>\s*opts\.WithIdentity\("([^"]+)"\)\);',s):
            rest=s[m.end():];nxt=rest.find('q.AddJob<');block=rest[:nxt] if nxt>=0 else rest
            cr=re.search(r'WithCronSchedule\("([^"]+)"',block);interval=re.search(r'WithIntervalIn(Seconds|Minutes|Hours)\((\d+)\)',block)
            scheduled.append({'service':item['path'].split('/')[0].split('.')[-1],'job_class':m.group(1),'identity':m.group(2),'source_path':item['path'],'line':s.count('\n',0,m.start())+1,'cron':cr.group(1) if cr else None,'interval':{'unit':interval.group(1).lower(),'value':int(interval.group(2))} if interval else None,'start_now':'.StartNow()' in block,'repeat_forever':'.RepeatForever()' in block,'repeat_count':int(re.search(r'WithRepeatCount\((\d+)\)',block).group(1)) if re.search(r'WithRepeatCount\((\d+)\)',block) else None,'runtime_timezone_and_cluster_semantics':'unresolved'})

databases=[]
for item in INV['groups']['databases']:
    s=(SOURCES/item['path']).read_text()
    databases.append({'path':item['path'],'blob':item['sha'],'source_url':URL+item['path'],'dbsets':[{'type':m.group(1),'name':m.group(2),'line':s.count('\n',0,m.start())+1} for m in re.finditer(r'DbSet<([^>]+)>\s+(\w+)',s)],'shared_filter_calls':[line.strip() for line in s.splitlines() if 'ApplySoftDeleteFilters' in line or 'ApplyModelBase' in line],'status':'declarations_only; complete field/default/index/migration and soft-delete contracts still required'})

raw_manifest=[]
expected={x['path']:x['sha'] for group in ['controllers','rpc','jobs','databases'] for x in INV['groups'][group]}
expected.update({'LICENSE.txt':'be3f7b28e564e7dd05eaf59d64adba1a4065ac0e','DysonNetwork.Wallet/Payment/WalletProductService.cs':'7dfc62726a8d63a8d4de69c2c0d962f6e08da2b1','DysonNetwork.Wallet/Models/WalletProductOptions.cs':'c9ddef8ea10178625e12a4b69e2cc309beb89452'})
for path,blob in sorted(expected.items()):
    f=SOURCES/path
    if not f.exists():continue
    b=f.read_bytes();sha=lambda b:hashlib.sha1(b'blob '+str(len(b)).encode()+b'\0'+b).hexdigest()
    if sha(b)!=blob:
        assert b.endswith(b'\n') and sha(b[:-1])==blob,('source hash mismatch',path)
        b=b[:-1];f.write_bytes(b)
    raw_manifest.append({'file':path,'blob':blob,'sha256':hashlib.sha256(b).hexdigest(),'bytes':len(b),'source_url':URL+path,'license':'AGPL-3.0; root LICENSE.txt; embedded third-party content requires independent review'})
(SOURCES/'manifest.json').write_text(json.dumps({'schema_version':1,'repository':'Solsynth/DysonNetwork','baseline':BASELINE,'source_policy':'Licensed root-repository files only; no Shared submodule, Spec schemas, descriptors, generated bindings, appsettings or production secrets. Fortune corpus snapshot is deliberately omitted.','files':raw_manifest,'omitted_sources':[{'file':FORTUNE,'blob':expected[FORTUNE],'reason':'Embedded quotation corpus permission unresolved; operation metadata retained without quotations.'}]},indent=2)+'\n')

# Editorial roadmap data is separately reviewable and contains no source body.
policy=json.loads((SOURCES/'coverage-policy.json').read_text())
family_counts=collections.Counter(o['family'] for o in operations)
for f in policy['families']:
    f['static_http_attribute_count']=family_counts[f['id']]
    f['operation_ids']=[o['id'] for o in operations if o['family']==f['id']]
for o in operations:
    family_policy=next(f for f in policy['families'] if f['id']==o['family'])
    o['required_release_gates']=list(dict.fromkeys(o['required_release_gates']+family_policy['release_gates']))
assert set(family_counts)=={f['id'] for f in policy['families']},set(family_counts)-{f['id'] for f in policy['families']}
service_counts=collections.Counter(o['service'] for o in operations)
plan={
 'schema_version':1,'prepared_on':'2026-10-08','baseline':{'upstream':BASELINE,'replacement_snapshot':'fcb3449501070745bbce4a0a3ba43a4df9ccf9bc'},
 'scope':'Full-program completion plan; static surface mapping for the five remaining DysonNetwork C# services, plus cross-backend integration work. This is not a claim of complete or production-ready Go replacement.',
 'measurement':{'controller_files':len(controllers),'controller_class_declarations':sum(len(c.get('controller_classes',[c['path']])) for c in controllers),'static_http_attributes':len(operations),'by_service':dict(sorted(service_counts.items())),'local_http_operations':policy['current_checkpoint']['local_http_operations'],'synthetic_candidate_http_operations':policy['current_checkpoint']['synthetic_candidate_http_operations'],'runtime_verified_endpoint_total':None,'full_backend_completion_percentage':None,'production_services_completed':0,'rpc_named_files':len(rpc_files),'rpc_implementation_files':len(rpc_files)-1,'rpc_override_declarations':sum(len(x['methods']) for x in rpc_files),'job_named_files':len(job_files),'literal_scheduled_job_registrations':len(scheduled),'database_files':len(databases),'dbset_declarations':sum(len(x['dbsets']) for x in databases),'migration_named_files':len(INV['groups']['migrations'])},
 'methodology':{'type':'Conservative lexical extraction plus manual family planning','limits':['818 counts static HTTP verb attributes, not distinct runtime endpoints or verified features.','Service route candidates combine literal attributes; gateway aliases, ASP.NET constraint semantics, action discovery and global filters remain unverified.','Observed body facts omit effects hidden in helper/service/dependency methods. A GET may mutate, and absence of a side-effect token is not proof of purity.','Class and action authorization declarations are recorded separately. No declaration does not establish anonymous access.','RPC override names/signatures describe licensed implementations only; they do not establish wire paths, protobuf numbers, serialization, streaming or compatibility.','Job filenames omit classes defined in differently named files. Literal registrations reveal additional jobs; hosted workers/listeners require separate tracing.','All source snapshots verify against expected Git blob SHA and SHA-256; Fortune corpus bytes are intentionally absent.','No appsettings, production databases/accounts, SoSYS requests, excluded schemas, generated code or provider operations were used.']},
 'public_documentation_followup':policy['public_documentation_followup'],'current_checkpoint':policy['current_checkpoint'],'production_blocker_groups':policy['production_blocker_groups'],'gates':policy['gates'],'phases':policy['phases'],'families':policy['families'],'independent_next_implementations':policy['independent_next_implementations'],'cross_backend_work':policy['cross_backend_work'],
 'http_operations':operations,'controller_index':[{k:v for k,v in c.items() if k!='operations'} for c in controllers],
 'rpc_implementations':rpc_files,'job_sources':job_files,'scheduled_job_registrations':scheduled,'databases':databases,
 'acceptance':policy['acceptance'],'evidence':['inventory/dyson-contract-files.json','inventory/dyson-service-registrations.json','inventory/operation-sources/manifest.json','docs/remaining-services-inventory.json','docs/parity-matrix.json','internal/auth/CONTRACT.md','internal/auth/HTTP_RECONSTRUCTION.md']}
from coverage_format import write_plan, read_plan
write_plan(ROOT/'docs/operation-coverage-plan.json',plan)
assert read_plan(ROOT/'docs/operation-coverage-plan.json') == plan
lines=['# Backend operation coverage plan','',f'Prepared 2026-10-08 against `Solsynth/DysonNetwork@{BASELINE}` and replacement snapshot `fcb3449501070745bbce4a0a3ba43a4df9ccf9bc`.','',plan['scope'],'','## What is mapped','',f"- {len(controllers)} controller files containing {plan['measurement']['controller_class_declarations']} controller class declarations and {len(operations)} static HTTP attributes. These are not 818 verified runtime endpoints.",'- Static attributes by service: '+', '.join(f'{k} {v}' for k,v in sorted(service_counts.items()))+'.',f"- {len(rpc_files)-1} licensed RPC implementation files, {plan['measurement']['rpc_override_declarations']} override declarations; the remaining RPC-named file is a test, not a service.",f"- {len(job_files)} job-named files, {len(scheduled)} literal job registrations, and {len(databases)} database files with {plan['measurement']['dbset_declarations']} DbSet declarations.",'- Runtime endpoint total and full-backend completion percentage remain unknown. Zero production services are certified complete by this plan.','', 'See [the machine-readable plan](operation-coverage-plan.json) for every operation, action signature, source line, declared authorization, observed error code/status, direct database member, gate and family.','', '## Current integrated checkpoint','',policy['current_checkpoint']['summary'],'','- Verification: '+policy['current_checkpoint']['verification'],'- Pending: '+policy['current_checkpoint']['pending'],'','## Independently published documentation','',policy['public_documentation_followup']['result'],'','See [the public-documentation audit](public-api-documentation-audit.md) for official links, retrieved factual evidence, version drift and exact remaining gaps.','', '## Evidence boundaries','']
lines+=['- '+x for x in plan['methodology']['limits']]
lines+=['','## Genuine blockers and bounded next steps','']
for b in policy['production_blocker_groups']:
    lines+=['### '+b['id']+' ('+b['kind']+')','',b['blocker'],'','Next step: '+b['bounded_next_step'],'']
lines+=['','## Current acceptance work and optional future implementations','']
for p in policy['independent_next_implementations']:
    lines+=['### '+p['name'],'',p['scope'],'','- Evidence: '+p['evidence'],'- Next implementation: '+p['next'],'- Tests: '+p['tests'],'- Remaining release blockers: '+p['release_blockers'],'']
lines+=['## Delivery phases','']
for p in policy['phases']:lines+=['### '+p['id']+': '+p['name'],'',p['work'],'','Exit evidence: '+p['exit'],'']
lines+=['## Family coverage and next work','']
for f in policy['families']:
    lines+=['### '+f['id']+f" ({f['static_http_attribute_count']} static HTTP attributes)",'',f['scope'],'','- Work available now: '+f['now'],'- Blocked compatibility facts: '+f['blockers'],'- Acceptance: '+f['acceptance'],'']
lines+=['## Cross-backend integration','']
for x in policy['cross_backend_work']:lines+=['- **'+x['name']+'**: '+x['work']]
lines+=['','## Release gates','']
for g in policy['gates']:lines+=['- **'+g['id']+'**: '+g['acceptance']]
lines+=['','## All HTTP static operation records','','The following is the complete controller-attribute index. Every row is a static candidate, not a verified endpoint. `?` in the action template is a route constraint/default, not an invented query parameter. Source links are pinned.','']
for service in sorted(service_counts):
    lines+=['### '+service,'','| Method | Service route candidate | Action and pinned source | Family |','|---|---|---|---|']
    for o in operations:
        if o['service']!=service:continue
        rt='<br>'.join('`'+r.replace('|','\\|')+'`' for r in o['service_route_candidates'])
        lines.append(f"| {o['http_method']} | {rt} | [{o['controller']}.{o['action']}:L{o['attribute_line']}]({o['source_url']}) | {o['family']} |")
    lines.append('')
lines+=['## RPC implementation method index','','Names below are implementation methods only. No protobuf definitions or generated bindings were inspected. Wire compatibility remains blocked.','']
for r in rpc_files:
    lines+=['- ['+r['path']+']('+URL+r['path']+'): '+(', '.join(m['name'] for m in r['methods']) or r['status'])]
lines+=['','## Scheduler registration index','','Schedules are literal source values; timezone, scheduler persistence, clustering, misfires and restarts are unverified.','']
for j in scheduled:
    sched=j['cron'] or (f"every {j['interval']['value']} {j['interval']['unit']}" if j['interval'] else 'start/repeat configuration; inspect source')
    lines+=['- '+j['service']+' / '+j['job_class']+': `'+sched+'`'+(' (start now)' if j['start_now'] else '')+'.']
lines+=['','## Completion criteria','']
lines+=['- '+x for x in policy['acceptance']]
(ROOT/'docs/operation-coverage-plan.md').write_text('\n'.join(lines)+'\n')
print(json.dumps({'counts':plan['measurement'],'retained_source_files':len(raw_manifest),'outputs':['docs/operation-coverage-plan.json','docs/operation-coverage-plan.md']},indent=2))

# Reproducible integrity checks are separate from implementation/runtime testing.
errors=[]
for entry in raw_manifest:
    data=(SOURCES/entry['file']).read_bytes()
    blob=hashlib.sha1(b'blob '+str(len(data)).encode()+b'\0'+data).hexdigest()
    if blob!=entry['blob'] or hashlib.sha256(data).hexdigest()!=entry['sha256']:errors.append('hash:'+entry['file'])
all_ids={o['id'] for o in operations};all_gates={g['id'] for g in policy['gates']}
for o in operations:
    if not o['signature'].startswith('public '):errors.append('signature:'+o['id'])
    if set(o['required_release_gates'])-all_gates:errors.append('gate:'+o['id'])
    if not all(r.startswith('/') for r in o['service_route_candidates']):errors.append('route:'+o['id'])
for f in policy['families']:
    if f['static_http_attribute_count']!=len(f['operation_ids']) or set(f['operation_ids'])-all_ids:errors.append('family:'+f['id'])
if (SOURCES/FORTUNE).exists():errors.append('fortune_corpus_retained')
route_dups=[str(k) for k,v in collections.Counter((o['service'],o['http_method'],r) for o in operations for r in o['service_route_candidates']).items() if v>1]
if route_dups:errors+=route_dups
current=[o for o in operations if o['implementation_status'] not in ['not_implemented_at_snapshot_baseline','embedded_corpus_permission_unresolved']]
if len(current)!=policy['current_checkpoint']['local_http_operations']:errors.append('local_operation_count')
report={'schema_version':1,'checked_on':'2026-10-08','scope':'Source/operation-plan integrity only; no implementation runtime validation','status':'passed' if not errors else 'failed','checks':{'retained_source_hashes':len(raw_manifest),'unique_static_http_records':len(all_ids),'controller_classes':plan['measurement']['controller_class_declarations'],'families':len(policy['families']),'all_family_memberships_reconcile':True,'gate_references_resolve':True,'literal_scheduled_job_registrations':len(scheduled),'rpc_override_declarations':plan['measurement']['rpc_override_declarations'],'local_operation_status_records':len(current),'synthetic_candidate_status_records':sum('synthetic' in o['implementation_status'] for o in operations),'full_fortune_corpus_absent':True,'duplicate_static_method_route_pairs':len(route_dups)},'output_sha256':{x:hashlib.sha256((ROOT/x).read_bytes()).hexdigest() for x in ['docs/operation-coverage-plan.json','docs/operation-coverage-plan.md']},'errors':errors}
(SOURCES/'validation.json').write_text(json.dumps(report,indent=2)+'\n')
assert not errors,errors
